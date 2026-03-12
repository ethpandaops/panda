package observability

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/config"
)

func TestServiceStartDisabledAndStopAreNoOps(t *testing.T) {
	t.Parallel()

	svc := NewService(logrus.New(), config.ObservabilityConfig{}).(*service)
	require.NoError(t, svc.Start(context.Background()))
	require.NoError(t, svc.Stop())
}

func TestServiceStartServesHealthReadyAndMetrics(t *testing.T) {
	t.Parallel()

	svc := NewService(logrus.New(), config.ObservabilityConfig{
		MetricsEnabled: true,
		MetricsPort:    freePort(t),
	}).(*service)

	require.NoError(t, svc.Start(context.Background()))

	baseURL := "http://127.0.0.1" + svc.server.Addr

	assert.Eventually(t, func() bool {
		resp, err := http.Get(baseURL + "/health")
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return false
		}

		return resp.StatusCode == http.StatusOK && string(body) == `{"status":"healthy"}`
	}, time.Second, 20*time.Millisecond)

	resp, err := http.Get(baseURL + "/ready")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, `{"status":"ready"}`, string(body))

	resp, err = http.Get(baseURL + "/metrics")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(body), "panda_tool_calls_total")

	err = svc.Start(context.Background())
	require.EqualError(t, err, "metrics server already started")

	require.NoError(t, svc.Stop())
	require.NoError(t, svc.Stop())
}

func TestServiceStopImmediatelyAfterStartDoesNotPanic(t *testing.T) {
	t.Parallel()

	svc := NewService(logrus.New(), config.ObservabilityConfig{
		MetricsEnabled: true,
		MetricsPort:    0,
	}).(*service)

	require.NoError(t, svc.Start(context.Background()))
	require.NoError(t, svc.Stop())
}

func freePort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()

	return listener.Addr().(*net.TCPAddr).Port
}
