package workflowrelay

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFilterHeadersKeepsOnlyAllowList(t *testing.T) {
	t.Parallel()

	in := http.Header{}
	in.Set("Accept", "text/event-stream")
	in.Set("Content-Type", "application/json")
	in.Add("Last-Event-ID", "42")
	in.Set("Authorization", "Bearer caller-token")
	in.Set("Cookie", "session=abc")
	in.Set("X-Panda-On-Behalf-Of", "someone")

	out := FilterHeaders(in)

	assert.Equal(t, "text/event-stream", out.Get("Accept"))
	assert.Equal(t, "application/json", out.Get("Content-Type"))
	assert.Equal(t, "42", out.Get("Last-Event-ID"))
	assert.Empty(t, out.Get("Authorization"))
	assert.Empty(t, out.Get("Cookie"))
	assert.Empty(t, out.Get("X-Panda-On-Behalf-Of"))

	// The returned map must be a copy: mutating it must not touch the input.
	out.Set("Accept", "mutated")
	assert.Equal(t, "text/event-stream", in.Get("Accept"))
}

func TestRejectTraversal(t *testing.T) {
	t.Parallel()

	for _, ok := range []string{
		"whiteboards/wb_1/state",
		"workflows/wf_1/runs/run_2/task-executions/tasks.loop.child[iter=0002]/steer",
		"",
	} {
		assert.NoError(t, RejectTraversal(ok), ok)
	}

	for _, bad := range []string{
		"..",
		"../secrets",
		"whiteboards/../admin",
		"whiteboards/..;/admin",
		`whiteboards\..\admin`,
		"a/b;jsessionid=1/c",
		"a/.../b",
	} {
		assert.Error(t, RejectTraversal(bad), bad)
	}
}

func TestWriteProblem(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	WriteProblem(rec, http.StatusBadGateway, "Bad Gateway", "proxy is unreachable")

	require.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	assert.JSONEq(t,
		`{"type":"about:blank","title":"Bad Gateway","status":502,"detail":"proxy is unreachable"}`,
		rec.Body.String())
}

func TestIsEventStream(t *testing.T) {
	t.Parallel()

	assert.True(t, IsEventStream("text/event-stream"))
	assert.True(t, IsEventStream("text/event-stream; charset=utf-8"))
	assert.False(t, IsEventStream("application/json"))
	assert.False(t, IsEventStream(""))
}
