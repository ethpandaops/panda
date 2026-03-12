package proxyserver

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sirupsen/logrus"
	simpleauth "github.com/ethpandaops/panda/pkg/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResponseCaptureTracksStatusBytesAndFlush(t *testing.T) {
	flusher := &flushRecorder{ResponseWriter: httptest.NewRecorder()}
	capture := &responseCapture{ResponseWriter: flusher, statusCode: http.StatusOK}

	capture.WriteHeader(http.StatusCreated)
	n, err := capture.Write([]byte("hello"))
	require.NoError(t, err)
	capture.Flush()

	assert.Equal(t, 5, n)
	assert.Equal(t, http.StatusCreated, capture.statusCode)
	assert.Equal(t, 5, capture.bytesWritten)
	assert.True(t, flusher.flushed)
}

func TestResolveUserLabelsAndExtractDatasourceType(t *testing.T) {
	user, org := resolveUserLabels(t.Context())
	assert.Equal(t, "anonymous", user)
	assert.Equal(t, "unknown", org)

	authSvc, err := simpleauth.NewSimpleService(logrus.New(), simpleauth.Config{
		Enabled: true,
		GitHub: &simpleauth.GitHubConfig{
			ClientID:     "client-id",
			ClientSecret: "client-secret",
		},
		Tokens: simpleauth.TokensConfig{SecretKey: "test-secret-key"},
	})
	require.NoError(t, err)

	token := issueOAuthAccessToken(t, "http://proxy.test", "test-secret-key", 42, "octocat")

	handler := authSvc.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, org := resolveUserLabels(r.Context())
		_, _ = w.Write([]byte(user + "/" + org + "/" + GetUserID(r.Context())))
	}))

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://proxy.test/clickhouse/query", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	handler.ServeHTTP(recorder, req)

	assert.Equal(t, "octocat/ethpandaops/42", recorder.Body.String())
	assert.Equal(t, "clickhouse", extractDatasourceType("/clickhouse/query"))
	assert.Equal(t, "ethnode", extractDatasourceType("/execution/hoodi/node"))
	assert.Equal(t, "unknown", extractDatasourceType("/custom/path"))
}

func TestMetricsMiddlewareRecordsAuthenticatedRequests(t *testing.T) {
	authSvc, err := simpleauth.NewSimpleService(logrus.New(), simpleauth.Config{
		Enabled: true,
		GitHub: &simpleauth.GitHubConfig{
			ClientID:     "client-id",
			ClientSecret: "client-secret",
		},
		Tokens: simpleauth.TokensConfig{SecretKey: "test-secret-key"},
	})
	require.NoError(t, err)

	token := issueOAuthAccessToken(t, "http://proxy.test", "test-secret-key", 7, "builder")
	handler := authSvc.Middleware()(metricsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("ok"))
	})))

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://proxy.test/loki/query", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Datasource", "logs")
	handler.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusAccepted, recorder.Code)
	assert.Equal(t, "ok", recorder.Body.String())
}

type flushRecorder struct {
	http.ResponseWriter
	flushed bool
}

func (f *flushRecorder) Flush() {
	f.flushed = true
}

var _ http.Flusher = (*flushRecorder)(nil)
