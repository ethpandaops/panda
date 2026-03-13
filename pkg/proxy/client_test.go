package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	authclient "github.com/ethpandaops/panda/pkg/auth/client"
	authstore "github.com/ethpandaops/panda/pkg/auth/store"
	"github.com/ethpandaops/panda/pkg/serverapi"
	"github.com/ethpandaops/panda/pkg/types"
	"github.com/sirupsen/logrus"
)

type stubCredentialStore struct {
	loadTokens      *authclient.Tokens
	loadErr         error
	accessToken     string
	accessTokenErr  error
	isAuthenticated bool
}

func (s *stubCredentialStore) Path() string { return "" }

func (s *stubCredentialStore) Save(*authclient.Tokens) error { return nil }

func (s *stubCredentialStore) Load() (*authclient.Tokens, error) {
	return s.loadTokens, s.loadErr
}

func (s *stubCredentialStore) Clear() error { return nil }

func (s *stubCredentialStore) EnsureAccessToken() (string, error) {
	return s.accessToken, s.accessTokenErr
}

func (s *stubCredentialStore) HasUsableCredentialsHint() bool {
	return s.isAuthenticated
}

var _ authstore.Store = (*stubCredentialStore)(nil)

func newTestProxyClient(t *testing.T, cfg ClientConfig) *proxyClient {
	t.Helper()

	logger := logrus.New()
	logger.SetOutput(io.Discard)

	client := NewClient(logger, cfg).(*proxyClient)
	client.cfg.DiscoveryInterval = 0

	return client
}

func TestProxyClientDiscover(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		store          *stubCredentialStore
		status         int
		responseBody   string
		wantAuthHeader string
		wantErrIs      error
		wantErrText    string
		checkState     func(t *testing.T, client *proxyClient)
	}{
		{
			name: "successfully loads datasource state and bearer token",
			store: &stubCredentialStore{
				loadTokens:  &authclient.Tokens{AccessToken: "proxy-token"},
				accessToken: "proxy-token",
			},
			status:         http.StatusOK,
			wantAuthHeader: "Bearer proxy-token",
			responseBody: `{
				"datasources": [
					{"type":"clickhouse","name":"ch-primary","description":"Main CH","metadata":{"region":"us-east-1"}},
					{"type":"clickhouse","name":"","description":"ignored"},
					{"type":"prometheus","name":"prom-main","description":"Prom"},
					{"type":"loki","name":"logs-main","description":"Logs"},
					{"type":"other","name":"ignored-other","description":"Ignored"}
				],
				"s3_bucket":"artifacts",
				"s3_public_url_prefix":"https://cdn.example.com",
				"ethnode_available":true
			}`,
			checkState: func(t *testing.T, client *proxyClient) {
				t.Helper()

				got := client.Datasources()
				want := serverapi.DatasourcesResponse{
					Datasources: []types.DatasourceInfo{
						{
							Type:        "clickhouse",
							Name:        "ch-primary",
							Description: "Main CH",
							Metadata:    map[string]string{"region": "us-east-1"},
						},
						{
							Type:        "prometheus",
							Name:        "prom-main",
							Description: "Prom",
						},
						{
							Type:        "loki",
							Name:        "logs-main",
							Description: "Logs",
						},
						{
							Type:        "other",
							Name:        "ignored-other",
							Description: "Ignored",
						},
					},
					S3Bucket:          "artifacts",
					S3PublicURLPrefix: "https://cdn.example.com",
					EthNodeAvailable:  true,
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("Datasources() = %#v, want %#v", got, want)
				}

				if gotNames, wantNames := client.ClickHouseDatasources(), []string{"ch-primary"}; !reflect.DeepEqual(gotNames, wantNames) {
					t.Fatalf("ClickHouseDatasources() = %v, want %v", gotNames, wantNames)
				}

				got.Datasources[0].Metadata["region"] = "mutated"
				refetched := client.Datasources()
				if refetched.Datasources[0].Metadata["region"] != "us-east-1" {
					t.Fatalf("Datasources() metadata was not cloned, got %q", refetched.Datasources[0].Metadata["region"])
				}
			},
		},
		{
			name:           "unauthorized response returns auth required sentinel",
			store:          &stubCredentialStore{loadTokens: &authclient.Tokens{AccessToken: "proxy-token"}, accessToken: "proxy-token"},
			status:         http.StatusUnauthorized,
			responseBody:   "login required\n",
			wantAuthHeader: "Bearer proxy-token",
			wantErrIs:      ErrAuthenticationRequired,
			wantErrText:    "login required",
		},
		{
			name:         "unexpected status includes response body",
			status:       http.StatusBadGateway,
			responseBody: "upstream failed",
			wantErrText:  "unexpected status 502: upstream failed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var gotAuthHeader string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/datasources" {
					http.NotFound(w, r)
					return
				}

				gotAuthHeader = r.Header.Get("Authorization")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.responseBody)
			}))
			defer server.Close()

			client := newTestProxyClient(t, ClientConfig{URL: server.URL})
			if tc.store != nil {
				client.credStore = tc.store
			}

			err := client.Discover(context.Background())
			if tc.wantErrText == "" && err != nil {
				t.Fatalf("Discover() unexpected error: %v", err)
			}
			if tc.wantErrText != "" {
				if err == nil {
					t.Fatalf("Discover() error = nil, want %q", tc.wantErrText)
				}
				if !strings.Contains(err.Error(), tc.wantErrText) {
					t.Fatalf("Discover() error = %q, want substring %q", err.Error(), tc.wantErrText)
				}
			}
			if tc.wantErrIs != nil && !errors.Is(err, tc.wantErrIs) {
				t.Fatalf("Discover() error = %v, want errors.Is(..., %v)", err, tc.wantErrIs)
			}

			if gotAuthHeader != tc.wantAuthHeader {
				t.Fatalf("Authorization header = %q, want %q", gotAuthHeader, tc.wantAuthHeader)
			}

			if tc.checkState != nil {
				tc.checkState(t, client)
			}
		})
	}
}

func TestProxyClientAuthorizeRequest(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		initialAuth string
		store       *stubCredentialStore
		wantAuth    string
		wantErrIs   error
		wantErrText string
	}{
		{
			name:        "preserves existing authorization header",
			initialAuth: "Basic abc123",
			wantAuth:    "Basic abc123",
		},
		{
			name: "attaches bearer token from credential store",
			store: &stubCredentialStore{
				loadTokens:  &authclient.Tokens{AccessToken: "proxy-token"},
				accessToken: "proxy-token",
			},
			wantAuth: "Bearer proxy-token",
		},
		{
			name:     "no auth configured leaves request unchanged",
			wantAuth: "",
		},
		{
			name:        "missing credentials returns auth required",
			store:       &stubCredentialStore{},
			wantErrIs:   ErrAuthenticationRequired,
			wantErrText: "authorizing proxy request",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client := newTestProxyClient(t, ClientConfig{URL: "http://proxy.test"})
			if tc.store != nil {
				client.credStore = tc.store
			}

			req := httptest.NewRequest(http.MethodGet, "http://proxy.test/datasources", nil)
			if tc.initialAuth != "" {
				req.Header.Set("Authorization", tc.initialAuth)
			}

			err := client.AuthorizeRequest(req)
			if tc.wantErrText == "" && err != nil {
				t.Fatalf("AuthorizeRequest() unexpected error: %v", err)
			}
			if tc.wantErrText != "" {
				if err == nil {
					t.Fatalf("AuthorizeRequest() error = nil, want substring %q", tc.wantErrText)
				}
				if !strings.Contains(err.Error(), tc.wantErrText) {
					t.Fatalf("AuthorizeRequest() error = %q, want substring %q", err.Error(), tc.wantErrText)
				}
			}
			if tc.wantErrIs != nil && !errors.Is(err, tc.wantErrIs) {
				t.Fatalf("AuthorizeRequest() error = %v, want errors.Is(..., %v)", err, tc.wantErrIs)
			}

			if got := req.Header.Get("Authorization"); got != tc.wantAuth {
				t.Fatalf("Authorization header = %q, want %q", got, tc.wantAuth)
			}
		})
	}
}

func TestProxyClientStartAllowsAuthenticationRequired(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/datasources" {
			http.NotFound(w, r)
			return
		}

		http.Error(w, "login required", http.StatusUnauthorized)
	}))
	defer server.Close()

	client := newTestProxyClient(t, ClientConfig{URL: server.URL})
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("Start() unexpected error: %v", err)
	}

	if got := client.Datasources().Datasources; got != nil {
		t.Fatalf("Datasources().Datasources after auth-required startup = %#v, want nil", got)
	}

	if err := client.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() unexpected error: %v", err)
	}
}

func TestProxyClientEnsureAuthenticated(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		store       *stubCredentialStore
		wantErrIs   error
		wantErrText string
	}{
		{
			name: "no auth configured is allowed",
		},
		{
			name: "valid credentials pass",
			store: &stubCredentialStore{
				loadTokens:  &authclient.Tokens{AccessToken: "proxy-token"},
				accessToken: "proxy-token",
			},
		},
		{
			name:        "missing credentials returns login guidance",
			store:       &stubCredentialStore{},
			wantErrIs:   ErrAuthenticationRequired,
			wantErrText: "not authenticated to proxy. Run `panda auth login` first",
		},
		{
			name: "store load failure is surfaced",
			store: &stubCredentialStore{
				loadErr: fmt.Errorf("credentials file unreadable"),
			},
			wantErrText: "loading stored credentials: credentials file unreadable",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client := newTestProxyClient(t, ClientConfig{URL: "http://proxy.test"})
			if tc.store != nil {
				client.credStore = tc.store
			}

			err := client.EnsureAuthenticated(context.Background())
			if tc.wantErrText == "" && err != nil {
				t.Fatalf("EnsureAuthenticated() unexpected error: %v", err)
			}
			if tc.wantErrText != "" {
				if err == nil {
					t.Fatalf("EnsureAuthenticated() error = nil, want substring %q", tc.wantErrText)
				}
				if !strings.Contains(err.Error(), tc.wantErrText) {
					t.Fatalf("EnsureAuthenticated() error = %q, want substring %q", err.Error(), tc.wantErrText)
				}
			}
			if tc.wantErrIs != nil && !errors.Is(err, tc.wantErrIs) {
				t.Fatalf("EnsureAuthenticated() error = %v, want errors.Is(..., %v)", err, tc.wantErrIs)
			}
		})
	}
}

func TestProxyClientStartLoadsDatasourcesFromProxy(t *testing.T) {
	t.Parallel()

	response := serverapi.DatasourcesResponse{
		Datasources: []types.DatasourceInfo{
			{Type: "clickhouse", Name: "ch-primary", Description: "Main CH"},
			{Type: "prometheus", Name: "prom-main", Description: "Main Prom"},
		},
		S3Bucket:         "artifacts",
		EthNodeAvailable: true,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/datasources" {
			http.NotFound(w, r)
			return
		}

		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Fatalf("encoding response: %v", err)
		}
	}))
	defer server.Close()

	client := newTestProxyClient(t, ClientConfig{URL: server.URL, HTTPTimeout: time.Second})
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("Start() unexpected error: %v", err)
	}
	defer func() {
		if err := client.Stop(context.Background()); err != nil {
			t.Fatalf("Stop() unexpected error: %v", err)
		}
	}()

	if got := client.Datasources(); !reflect.DeepEqual(got, response) {
		t.Fatalf("Datasources() = %#v, want %#v", got, response)
	}
}
