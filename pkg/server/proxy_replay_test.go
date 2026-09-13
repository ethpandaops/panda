package server

import (
	"bytes"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/proxy"
	"github.com/ethpandaops/panda/pkg/types"
)

// sequenceTransport answers each request with the next queued status, then
// repeats the last one. It counts round trips so tests can assert on replays.
type sequenceTransport struct {
	statuses []int
	bodies   []string
	calls    int
}

func (t *sequenceTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	idx := t.calls
	if idx >= len(t.statuses) {
		idx = len(t.statuses) - 1
	}

	t.calls++

	resp := &http.Response{
		StatusCode: t.statuses[idx],
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(t.bodies[idx])),
	}

	return resp, nil
}

// invalidatingProxyClient counts Invalidate calls on top of the routing fake.
type invalidatingProxyClient struct {
	routingProxyClient
	invalidations int
}

func (c *invalidatingProxyClient) Invalidate() { c.invalidations++ }

func newReplayComputeService(t *testing.T, transport http.RoundTripper) (*service, *invalidatingProxyClient) {
	t.Helper()

	client := &invalidatingProxyClient{
		routingProxyClient: routingProxyClient{
			url:     "https://hosted.proxy",
			token:   "hosted-token",
			compute: []types.DatasourceInfo{{Name: "production"}},
		},
	}

	svc := testRoutingService(t, transport, []proxy.ClientRoute{{Name: "hosted", Client: client}})

	return svc, client
}

// TestComputeExecNotReplayedOnAuthRejection guards against silently running a
// guest command twice: an auth rejection must refresh the token and surface a
// retry hint instead of re-sending the exec.
func TestComputeExecNotReplayedOnAuthRejection(t *testing.T) {
	t.Parallel()

	transport := &sequenceTransport{
		statuses: []int{http.StatusUnauthorized, http.StatusOK},
		bodies:   []string{`{"error":"token expired"}`, `{"exit_code":0}`},
	}
	svc, client := newReplayComputeService(t, transport)

	rec := callComputeOp(t, svc, "compute.exec_sandbox", map[string]any{
		"id":      "sb-1",
		"command": []any{"rm", "-rf", "/tmp/scratch"},
	})

	assert.Equal(t, 1, transport.calls, "a non-replayable request must not be re-sent")
	assert.Equal(t, 1, client.invalidations, "the cached token must still be invalidated")
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Contains(t, rec.Body.String(), "authentication expired mid-request")
	assert.Contains(t, rec.Body.String(), "retry the command")
}

// TestComputeReadReplaysOnAuthRejection verifies read-only compute requests
// keep the one invalidate-and-retry on auth rejection.
func TestComputeReadReplaysOnAuthRejection(t *testing.T) {
	t.Parallel()

	transport := &sequenceTransport{
		statuses: []int{http.StatusUnauthorized, http.StatusOK},
		bodies:   []string{`{"error":"token expired"}`, `{"id":"sb-1"}`},
	}
	svc, client := newReplayComputeService(t, transport)

	rec := callComputeOp(t, svc, "compute.get_sandbox", map[string]any{"id": "sb-1"})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, 2, transport.calls, "a replayable request retries once after invalidation")
	assert.Equal(t, 1, client.invalidations)
	assert.JSONEq(t, `{"id":"sb-1"}`, rec.Body.String())
}
