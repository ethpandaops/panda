package handlers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClickHouseHandlerProxiesViaRewrite verifies the Rewrite-based reverse proxy
// forwards to the target host, injects the default database, preserves the
// original query, strips the /clickhouse prefix, and rewrites auth + X-Forwarded.
func TestClickHouseHandlerProxiesViaRewrite(t *testing.T) {
	var got struct {
		host, path, rawQuery, auth, xff string
	}

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.host = r.Host
		got.path = r.URL.Path
		got.rawQuery = r.URL.RawQuery
		got.auth = r.Header.Get("Authorization")
		got.xff = r.Header.Get("X-Forwarded-For")

		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer backend.Close()

	u, err := url.Parse(backend.URL)
	require.NoError(t, err)

	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)

	h := NewClickHouseHandler(logrus.New(), []ClickHouseConfig{{
		Name:     "clickhouse-raw",
		Host:     u.Hostname(),
		Port:     port,
		Secure:   false,
		Database: "default",
		Username: "user",
		Password: "pass",
	}})

	req := httptest.NewRequest(http.MethodGet, "/clickhouse/?query=SELECT+1", nil)
	req.Header.Set(DatasourceHeader, "clickhouse-raw")
	req.Header.Set("Authorization", "Bearer sandbox-token")
	req.RemoteAddr = "203.0.113.9:5555"

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	res := rec.Result()
	defer func() { _ = res.Body.Close() }()

	require.Equal(t, http.StatusOK, res.StatusCode)
	assert.Equal(t, u.Host, got.host)                    // Host rewritten to target
	assert.Equal(t, "/", got.path)                       // /clickhouse prefix stripped
	assert.Contains(t, got.rawQuery, "database=default") // default database injected
	assert.Contains(t, got.rawQuery, "query=SELECT+1")   // original query preserved
	assert.NotEqual(t, "Bearer sandbox-token", got.auth) // inbound bearer stripped
	assert.True(t, strings.HasPrefix(got.auth, "Basic "), "expected basic auth, got %q", got.auth)
	assert.Equal(t, "203.0.113.9", got.xff) // SetXForwarded
}
