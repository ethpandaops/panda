package cli

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildSessionSendBodyMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		interrupt bool
		wantMode  string
	}{
		{name: "default queue", interrupt: false, wantMode: "queue"},
		{name: "interrupt stop_and_send", interrupt: true, wantMode: "stop_and_send"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body, err := buildSessionSendBody("hello", tt.interrupt)
			require.NoError(t, err)

			var payload map[string]any
			require.NoError(t, json.Unmarshal(body, &payload))

			assert.Equal(t, "message", payload["type"])
			assert.Equal(t, tt.wantMode, payload["mode"])
			assert.Equal(t, "hello", payload["content"])
		})
	}
}

func TestBuildSessionCreateBodyOmitsInitialItemWithoutContent(t *testing.T) {
	t.Parallel()

	body, err := buildSessionCreateBody("", "")
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload))

	_, hasInitialItem := payload["initialItem"]
	assert.False(t, hasInitialItem, "no --content must send NO initialItem")
	assert.JSONEq(t, `{}`, string(body))
}

func TestBuildSessionCreateBodyWithContent(t *testing.T) {
	t.Parallel()

	body, err := buildSessionCreateBody("my title", "do the thing")
	require.NoError(t, err)

	var payload struct {
		Title       string `json:"title"`
		InitialItem struct {
			Type    string `json:"type"`
			Mode    string `json:"mode"`
			Content string `json:"content"`
		} `json:"initialItem"`
	}

	require.NoError(t, json.Unmarshal(body, &payload))

	assert.Equal(t, "my title", payload.Title)
	assert.Equal(t, "message", payload.InitialItem.Type)
	assert.Equal(t, "queue", payload.InitialItem.Mode)
	assert.Equal(t, "do the thing", payload.InitialItem.Content)
}

func TestBuildRunBodyEmbedsInputsAndDispatch(t *testing.T) {
	t.Parallel()

	body, err := buildRunBody(`{"values":{"a":1}}`, `{"defaults":{"agent":"x"}}`)
	require.NoError(t, err)

	assert.JSONEq(t,
		`{"inputs":{"values":{"a":1}},"dispatchPolicy":{"defaults":{"agent":"x"}}}`,
		string(body))
}

func TestBuildRunBodyEmptyIsEmptyObject(t *testing.T) {
	t.Parallel()

	body, err := buildRunBody("", "")
	require.NoError(t, err)

	assert.JSONEq(t, `{}`, string(body))
}

func TestDispatchEffectiveQueryOmitsScopeUnlessSet(t *testing.T) {
	t.Parallel()

	assert.Nil(t, dispatchEffectiveQuery(""), "no --scope must send no scope param")

	query := dispatchEffectiveQuery("org")
	require.NotNil(t, query)
	assert.Equal(t, "org", query.Get("scope"))
}

func TestNormalizeWorkflowAPIPathStripsPrefix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		raw          string
		wantSegments []string
		wantQuery    map[string]string
		wantErr      string
	}{
		{
			name:         "bare relative path",
			raw:          "whiteboards",
			wantSegments: []string{"whiteboards"},
		},
		{
			name:         "leading slash stripped",
			raw:          "/whiteboards",
			wantSegments: []string{"whiteboards"},
		},
		{
			name:         "leading /api/v1 stripped",
			raw:          "/api/v1/whiteboards/wb_1/state",
			wantSegments: []string{"whiteboards", "wb_1", "state"},
		},
		{
			name:         "api/v1 without leading slash stripped",
			raw:          "api/v1/workflows",
			wantSegments: []string{"workflows"},
		},
		{
			// Finding F: api/v1 strip must be segment-anchored — 'api/v1foo' is
			// NOT the prefix, so it is left intact (splits on '/').
			name:         "api/v1foo not mis-stripped",
			raw:          "api/v1foo/bar",
			wantSegments: []string{"api", "v1foo", "bar"},
		},
		{
			name:         "query preserved",
			raw:          "runs?limit=5",
			wantSegments: []string{"runs"},
			wantQuery:    map[string]string{"limit": "5"},
		},
		{
			// Finding F: a malformed query is surfaced, not silently dropped.
			name:    "malformed query surfaced as error",
			raw:     "runs?%zz",
			wantErr: "parsing query",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			segments, query, err := normalizeWorkflowAPIPath(tt.raw)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantSegments, segments)

			for k, v := range tt.wantQuery {
				assert.Equal(t, v, query.Get(k))
			}
		})
	}
}

func TestQueueItemActionMapping(t *testing.T) {
	t.Parallel()

	method, action, err := queueItemAction(true, false, false)
	require.NoError(t, err)
	assert.Equal(t, "POST", method)
	assert.Equal(t, "retry", action)

	method, action, err = queueItemAction(false, true, false)
	require.NoError(t, err)
	assert.Equal(t, "POST", method)
	assert.Equal(t, "skip", action)

	method, action, err = queueItemAction(false, false, true)
	require.NoError(t, err)
	assert.Equal(t, "DELETE", method)
	assert.Equal(t, "", action)

	_, _, err = queueItemAction(false, false, false)
	require.Error(t, err)

	_, _, err = queueItemAction(true, true, false)
	require.Error(t, err)
}

func TestSteerQueueItemAction(t *testing.T) {
	t.Parallel()

	action, err := steerQueueItemAction(true, false)
	require.NoError(t, err)
	assert.Equal(t, "dismiss", action)

	action, err = steerQueueItemAction(false, true)
	require.NoError(t, err)
	assert.Equal(t, "retry", action)

	_, err = steerQueueItemAction(false, false)
	require.Error(t, err)

	_, err = steerQueueItemAction(true, true)
	require.Error(t, err)
}

func TestWorkerLogQueryFilters(t *testing.T) {
	t.Parallel()

	assert.Nil(t, workerLogQuery("", nil, nil))

	query := workerLogQuery("cur1", []string{"tasks.x"}, []string{"te_1"})
	require.NotNil(t, query)
	assert.Equal(t, "cur1", query.Get("cursor"))
	assert.Equal(t, []string{"tasks.x"}, query["specNodeIds[]"])
	assert.Equal(t, []string{"te_1"}, query["taskExecutionIds[]"])
}

func TestBuildWhiteboardCreateBody(t *testing.T) {
	t.Parallel()

	body, err := buildWhiteboardCreateBody("wb", "count blocks", []byte(`{"x":1}`))
	require.NoError(t, err)

	assert.JSONEq(t, `{"name":"wb","requirements":"count blocks","inputs":{"x":1}}`, string(body))

	empty, err := buildWhiteboardCreateBody("", "", nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(empty))
}

func TestRunStatusExitError(t *testing.T) {
	t.Parallel()

	assert.NoError(t, runStatusExitError("completed"))

	for _, status := range []string{"failed", "cancelled"} {
		err := runStatusExitError(status)
		require.Error(t, err)

		var exitErr *exitCodeError
		require.ErrorAs(t, err, &exitErr)
		assert.Equal(t, 1, exitErr.code)
	}
}
