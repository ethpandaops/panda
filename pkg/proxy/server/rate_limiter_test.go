package proxyserver

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	logrustest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRateLimiterAllowCleanupAndStop(t *testing.T) {
	logger, _ := logrustest.NewNullLogger()
	rl := NewRateLimiter(logger, RateLimiterConfig{
		RequestsPerMinute: 60,
		BurstSize:         1,
	})

	first := rl.getLimiter("user-1")
	second := rl.getLimiter("user-1")
	require.Same(t, first, second)

	assert.True(t, rl.Allow("user-1"))
	assert.False(t, rl.Allow("user-1"))

	rl.cleanup()
	assert.Contains(t, rl.limiters, "user-1")

	idle := rl.getLimiter("user-2")
	require.NotNil(t, idle)
	rl.cleanup()
	assert.NotContains(t, rl.limiters, "user-2")

	rl.Stop()
	rl.Stop()
}

func TestRateLimiterMiddlewareAndAuditorHelpers(t *testing.T) {
	logger, hook := logrustest.NewNullLogger()
	rl := NewRateLimiter(logger, RateLimiterConfig{
		RequestsPerMinute: 60,
		BurstSize:         1,
	})
	t.Cleanup(rl.Stop)

	passthrough := rl.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://proxy.test/datasources", nil)
	passthrough.ServeHTTP(recorder, req)
	assert.Equal(t, http.StatusNoContent, recorder.Code)

	bodyReq := httptest.NewRequest(http.MethodPost, "http://proxy.test/clickhouse/query", strings.NewReader(" SELECT 1 "))
	snapshot := captureBody(bodyReq, 32)
	assert.Equal(t, "SELECT 1", snapshot)
	restored, err := io.ReadAll(bodyReq.Body)
	require.NoError(t, err)
	assert.Equal(t, " SELECT 1 ", string(restored))

	assert.True(t, isS3Route("/s3/bucket/key"))
	assert.False(t, isS3Route("/clickhouse/query"))
	assert.Equal(t, "up", extractQuery(httptest.NewRequest(http.MethodGet, "http://proxy.test/prometheus/query?query=up", nil), ""))
	assert.Equal(t, "SELECT 1", extractQuery(httptest.NewRequest(http.MethodPost, "http://proxy.test/clickhouse/query", nil), "SELECT 1"))

	auditor := NewAuditor(logger, AuditorConfig{LogQueries: true, MaxQueryLength: 8})
	auditHandler := auditor.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))

	auditRecorder := httptest.NewRecorder()
	auditReq := httptest.NewRequest(http.MethodPost, "http://proxy.test/clickhouse/query", strings.NewReader("SELECT * FROM blocks"))
	auditHandler.ServeHTTP(auditRecorder, auditReq)

	require.NotEmpty(t, hook.AllEntries())
	assert.Equal(t, "Audit", hook.LastEntry().Message)
	assert.Equal(t, "clickhouse", hook.LastEntry().Data["datasource"])
	assert.Contains(t, hook.LastEntry().Data["query"], "SELECT *")
}
