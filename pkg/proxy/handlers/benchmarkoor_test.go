package handlers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBenchmarkoorHandlerProxiesViaRewrite verifies the reverse proxy forwards
// requests to the upstream correctly: the /benchmarkoor path prefix is
// stripped, the inbound Authorization and Cookie headers are replaced with the
// configured API key, and the target Host is set.
func TestBenchmarkoorHandlerProxiesViaRewrite(t *testing.T) {
	var got struct {
		host, path, rawQuery, auth, cookie string
	}

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.host = r.Host
		got.path = r.URL.Path
		got.rawQuery = r.URL.RawQuery
		got.auth = r.Header.Get("Authorization")
		got.cookie = r.Header.Get("Cookie")

		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "backend-ok")
	}))
	defer backend.Close()

	backendURL, err := url.Parse(backend.URL)
	require.NoError(t, err)

	h := NewBenchmarkoorHandler(logrus.New(), []BenchmarkoorConfig{{
		Name:   "production",
		URL:    backend.URL,
		APIKey: "bmk_test_key",
	}})

	req := httptest.NewRequest(http.MethodGet, "/benchmarkoor/api/v1/index/query/runs?limit=1", nil)
	req.Header.Set(DatasourceHeader, "production")
	req.Header.Set("Authorization", "Bearer sandbox-token")
	req.Header.Set("Cookie", "benchmarkoor_session=stolen")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	res := rec.Result()
	defer func() { _ = res.Body.Close() }()

	body, _ := io.ReadAll(res.Body)

	require.Equal(t, http.StatusOK, res.StatusCode, "proxy should return the backend status")
	assert.Equal(t, "backend-ok", string(body))

	// /benchmarkoor prefix stripped, rest of the path and the query preserved.
	assert.Equal(t, "/api/v1/index/query/runs", got.path)
	assert.Equal(t, "limit=1", got.rawQuery)

	// Host rewritten to the upstream target.
	assert.Equal(t, backendURL.Host, got.host)

	// Inbound bearer token replaced with the configured API key; cookies dropped.
	assert.Equal(t, "Bearer bmk_test_key", got.auth)
	assert.Empty(t, got.cookie)
}

// TestBenchmarkoorHandlerIsReadOnly verifies non-GET/HEAD methods are rejected
// before reaching the upstream.
func TestBenchmarkoorHandlerIsReadOnly(t *testing.T) {
	backendHits := 0

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	h := NewBenchmarkoorHandler(logrus.New(), []BenchmarkoorConfig{{
		Name: "production",
		URL:  backend.URL,
	}})

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req := httptest.NewRequest(method, "/benchmarkoor/api/v1/auth/api-keys", nil)
		req.Header.Set(DatasourceHeader, "production")

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code, "method %s should be rejected", method)
	}

	assert.Zero(t, backendHits, "write methods must never reach the upstream")
}

// TestBenchmarkoorHandlerUnknownDatasource verifies routing by the
// X-Datasource header.
func TestBenchmarkoorHandlerUnknownDatasource(t *testing.T) {
	h := NewBenchmarkoorHandler(logrus.New(), []BenchmarkoorConfig{{
		Name: "production",
		URL:  "http://127.0.0.1:0",
	}})

	req := httptest.NewRequest(http.MethodGet, "/benchmarkoor/api/v1/health", nil)
	req.Header.Set(DatasourceHeader, "other")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}
