package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sirupsen/logrus"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/execsvc"
	"github.com/ethpandaops/panda/pkg/resource"
	"github.com/ethpandaops/panda/pkg/sandbox"
	"github.com/ethpandaops/panda/pkg/serverapi"
	"github.com/ethpandaops/panda/pkg/storage"
	"github.com/ethpandaops/panda/pkg/tokenstore"
	"github.com/ethpandaops/panda/pkg/types"
)

func TestRuntimeAuthMiddleware(t *testing.T) {
	tokens := tokenstore.New(time.Hour)
	t.Cleanup(tokens.Stop)

	srv := &service{runtimeTokens: tokens}

	t.Run("rejects missing and invalid tokens", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/storage/files", nil)

		srv.runtimeAuthMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Fatal("next handler should not run")
		})).ServeHTTP(rec, req)

		require.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.Contains(t, rec.Body.String(), "missing runtime Authorization header")

		rec = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodGet, "/api/v1/runtime/storage/files", nil)
		req.Header.Set("Authorization", "Bearer invalid")

		srv.runtimeAuthMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Fatal("next handler should not run")
		})).ServeHTTP(rec, req)

		require.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.Contains(t, rec.Body.String(), "invalid or expired runtime token")
	})

	t.Run("injects execution id for valid tokens", func(t *testing.T) {
		token := tokens.Register("exec-1")
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/storage/files", nil)
		req.Header.Set("Authorization", "Bearer "+token)

		srv.runtimeAuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "exec-1", runtimeExecutionID(r.Context()))
			w.WriteHeader(http.StatusNoContent)
		})).ServeHTTP(rec, req)

		assert.Equal(t, http.StatusNoContent, rec.Code)
	})
}

func TestAPIHandlersExecuteSessionsAndDatasources(t *testing.T) {
	registry := resource.NewRegistry(logrus.New())
	stubSandbox := &apiStubSandbox{
		executeResult: &sandbox.ExecutionResult{
			Stdout:              "hello\n",
			ExitCode:            0,
			ExecutionID:         "exec-1",
			SessionID:           "session-1",
			SessionTTLRemaining: 9 * time.Minute,
		},
		sessions: []sandbox.SessionInfo{{
			ID:           "session-1",
			CreatedAt:    time.Unix(10, 0).UTC(),
			LastUsed:     time.Unix(20, 0).UTC(),
			TTLRemaining: 5 * time.Minute,
		}},
		createdSession: &sandbox.CreatedSession{
			ID:           "session-2",
			TTLRemaining: 10 * time.Minute,
		},
	}
	tokens := tokenstore.New(time.Hour)
	t.Cleanup(tokens.Stop)

	srv := &service{
		log:               logrus.New(),
		proxyService:      &apiStubProxyService{datasourceInfo: []types.DatasourceInfo{{Type: "clickhouse", Name: "xatu"}, {Type: "loki", Name: "logs"}}},
		proxyAuthMetadata: &serverapi.ProxyAuthMetadataResponse{Enabled: true, IssuerURL: "https://issuer.example"},
		execService: execsvc.New(
			logrus.New(),
			stubSandbox,
			apiStubEnvBuilder{},
			30,
			tokens,
		),
		resourceRegistry: registry,
	}

	t.Run("datasources and proxy auth metadata", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/datasources?type=clickhouse", nil)
		srv.handleAPIDatasources(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)

		var response serverapi.DatasourcesResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
		require.Len(t, response.Datasources, 1)
		assert.Equal(t, "xatu", response.Datasources[0].Name)

		rec = httptest.NewRecorder()
		srv.handleAPIProxyAuthMetadata(rec, httptest.NewRequest(http.MethodGet, "/api/v1/proxy/auth", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), "issuer.example")
	})

	t.Run("execute and session lifecycle handlers", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/execute", strings.NewReader(`{"code":"print('hello')"}`))
		srv.handleAPIExecute(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)

		var executeResponse serverapi.ExecuteResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &executeResponse))
		assert.Equal(t, "hello\n", executeResponse.Stdout)
		assert.Equal(t, "session-1", executeResponse.SessionID)
		assert.Equal(t, "9m0s", executeResponse.SessionTTLRemaining)

		rec = httptest.NewRecorder()
		srv.handleAPIListSessions(rec, httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), "session-1")

		rec = httptest.NewRecorder()
		srv.handleAPICreateSession(rec, httptest.NewRequest(http.MethodPost, "/api/v1/sessions", nil))
		require.Equal(t, http.StatusCreated, rec.Code)
		assert.Contains(t, rec.Body.String(), "session-2")

		rec = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodDelete, "/api/v1/sessions/session-2", nil)
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("sessionID", "session-2")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
		srv.handleAPIDestroySession(rec, req)
		assert.Equal(t, http.StatusNoContent, rec.Code)
		assert.Equal(t, []string{"session-2"}, stubSandbox.destroyedSessions)
	})
}

func TestAPIResourceStorageAndProxyHelpers(t *testing.T) {
	registry := resource.NewRegistry(logrus.New())
	registry.RegisterStatic(types.StaticResource{
		Resource: mcp.NewResource("plain://hello", "Hello"),
		Handler: func(context.Context, string) (string, error) {
			return "hello world", nil
		},
	})

	store := tokenstore.New(time.Hour)
	t.Cleanup(store.Stop)

	storageSvc := storage.New(afero.NewMemMapFs(), "/tmp/storage", "http://server.example")
	srv := &service{
		log:              logrus.New(),
		resourceRegistry: registry,
		storageService:   storageSvc,
		proxyService: &apiStubProxyService{
			url:           "http://proxy.example",
			registerToken: "server-token",
		},
		runtimeTokens: store,
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(req.Body)
				require.NoError(t, err)
				assert.Equal(t, "/upstream", req.URL.Path)
				if req.Header.Get("Authorization") == "" {
					t.Fatal("expected authorization header")
				}

				return jsonHTTPResponse(http.StatusOK, req.Header.Clone(), string(body)), nil
			}),
		},
	}

	t.Run("reads resources and runtime storage endpoints", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/resources/read?uri=plain://hello", nil)
		srv.handleAPIReadResource(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "text/plain; charset=utf-8", rec.Header().Get("Content-Type"))
		assert.Equal(t, "hello world", rec.Body.String())

		ctx := context.WithValue(context.Background(), runtimeExecutionIDKey, "exec-1")
		rec = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodPost, "/api/v1/runtime/storage/upload?name=/plots/figure.txt", strings.NewReader("figure"))
		req = req.WithContext(ctx)
		srv.handleRuntimeStorageUpload(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), "plots/figure.txt")

		rec = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodGet, "/api/v1/runtime/storage/files?prefix=plots", nil)
		req = req.WithContext(ctx)
		srv.handleRuntimeStorageList(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), "plots/figure.txt")

		rec = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodGet, "/api/v1/runtime/storage/url?key=plots/figure.txt", nil)
		req = req.WithContext(ctx)
		srv.handleRuntimeStorageURL(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), "http://server.example/api/v1/storage/files/exec-1/plots/figure.txt")

		rec = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodGet, "/api/v1/storage/files/exec-1/plots/figure.txt", nil)
		srv.handleStorageServeFile(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "figure", rec.Body.String())
	})

	t.Run("forwards proxy requests and helper utilities", func(t *testing.T) {
		data, status, headers, err := srv.proxyRequest(
			context.Background(),
			http.MethodPost,
			"/upstream",
			bytes.NewReader([]byte("payload")),
			http.Header{"X-Test": []string{"yes"}},
		)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, status)
		assert.Equal(t, "application/json", headers.Get("Content-Type"))
		assert.Contains(t, string(data), "payload")

		value, err := parseOptionalInt(httptest.NewRequest(http.MethodGet, "/?limit=7", nil), "limit")
		require.NoError(t, err)
		assert.Equal(t, 7, value)

		err = decodeJSON(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"ok"}`)), &map[string]string{})
		require.NoError(t, err)
		assert.Equal(t, "dora", operationExtensionName("dora.link_slot"))
		assert.Equal(t, "", operationExtensionName("invalid"))
	})
}

type apiStubEnvBuilder struct{}

func (apiStubEnvBuilder) BuildSandboxEnv() (map[string]string, error) {
	return map[string]string{"ETHPANDAOPS_API_URL": "http://server.example"}, nil
}

type apiStubSandbox struct {
	executeResult     *sandbox.ExecutionResult
	sessions          []sandbox.SessionInfo
	createdSession    *sandbox.CreatedSession
	destroyedSessions []string
}

func (s *apiStubSandbox) Start(context.Context) error { return nil }
func (s *apiStubSandbox) Stop(context.Context) error  { return nil }
func (s *apiStubSandbox) Execute(context.Context, sandbox.ExecuteRequest) (*sandbox.ExecutionResult, error) {
	return s.executeResult, nil
}
func (s *apiStubSandbox) Name() string { return "stub" }
func (s *apiStubSandbox) ListSessions(context.Context, string) ([]sandbox.SessionInfo, error) {
	return s.sessions, nil
}
func (s *apiStubSandbox) CreateSession(context.Context, string, map[string]string) (*sandbox.CreatedSession, error) {
	return s.createdSession, nil
}
func (s *apiStubSandbox) DestroySession(_ context.Context, sessionID, _ string) error {
	s.destroyedSessions = append(s.destroyedSessions, sessionID)
	return nil
}
func (s *apiStubSandbox) CanCreateSession(context.Context, string) (bool, int, int) {
	return true, len(s.sessions), 10
}
func (s *apiStubSandbox) SessionsEnabled() bool { return true }

type apiStubProxyService struct {
	url            string
	datasourceInfo []types.DatasourceInfo
	registerToken  string
}

func (s *apiStubProxyService) Start(context.Context) error          { return nil }
func (s *apiStubProxyService) Stop(context.Context) error           { return nil }
func (s *apiStubProxyService) URL() string                          { return s.url }
func (s *apiStubProxyService) AuthorizeRequest(*http.Request) error { return nil }
func (s *apiStubProxyService) RegisterToken(string) string          { return s.registerToken }
func (s *apiStubProxyService) RevokeToken(string)                   {}
func (s *apiStubProxyService) ClickHouseDatasources() []string      { return nil }
func (s *apiStubProxyService) ClickHouseDatasourceInfo() []types.DatasourceInfo {
	return nil
}
func (s *apiStubProxyService) PrometheusDatasources() []string { return nil }
func (s *apiStubProxyService) PrometheusDatasourceInfo() []types.DatasourceInfo {
	return nil
}
func (s *apiStubProxyService) LokiDatasources() []string { return nil }
func (s *apiStubProxyService) LokiDatasourceInfo() []types.DatasourceInfo {
	return nil
}
func (s *apiStubProxyService) S3Bucket() string                       { return "" }
func (s *apiStubProxyService) S3PublicURLPrefix() string              { return "" }
func (s *apiStubProxyService) EthNodeAvailable() bool                 { return false }
func (s *apiStubProxyService) DatasourceInfo() []types.DatasourceInfo { return s.datasourceInfo }
func (s *apiStubProxyService) Datasources() serverapi.DatasourcesResponse {
	return serverapi.DatasourcesResponse{Datasources: s.datasourceInfo}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func jsonHTTPResponse(status int, headers http.Header, body string) *http.Response {
	cloned := headers.Clone()
	cloned.Set("Content-Type", "application/json")

	return &http.Response{
		StatusCode: status,
		Header:     cloned,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
