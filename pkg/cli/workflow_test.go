package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/attribution"
)

func TestWorkflowPathPercentEncodesSegments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		segments []string
		want     string
	}{
		{
			name:     "plain segments",
			segments: []string{"whiteboards", "wb_123", "state"},
			want:     "/api/v1/workflow/whiteboards/wb_123/state",
		},
		{
			name: "steer loop iteration key with reserved chars",
			segments: []string{
				"workflows", "wf_1", "runs", "run_1", "task-executions",
				"tasks.fetch_weather.fetch_one[iter=0002]", "queue",
				"tlitem1.abc", "dismiss",
			},
			want: "/api/v1/workflow/workflows/wf_1/runs/run_1/task-executions/" +
				"tasks.fetch_weather.fetch_one%5Biter%3D0002%5D/queue/tlitem1.abc/dismiss",
		},
		{
			name:     "slash inside an id is escaped",
			segments: []string{"whiteboards", "a/b"},
			want:     "/api/v1/workflow/whiteboards/a%2Fb",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, workflowPath(tt.segments...))
		})
	}
}

func TestApiErrorMessageFallbackChain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "panda error field",
			body: `{"error":"boom"}`,
			want: "boom",
		},
		{
			name: "rfc7807 detail",
			body: `{"type":"about:blank","title":"Not Found","status":404,"detail":"run not found"}`,
			want: "run not found",
		},
		{
			name: "rfc7807 title only",
			body: `{"type":"about:blank","title":"Unauthorized","status":401}`,
			want: "Unauthorized",
		},
		{
			name: "text/plain unknown route 404 falls back to raw body",
			body: "404 page not found\n",
			want: "404 page not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, apiErrorMessage([]byte(tt.body)))
		})
	}
}

func TestDecodeAPIErrorTextPlain404NoPanic(t *testing.T) {
	t.Parallel()

	// An unknown-route text/plain 404 must not panic a JSON decode and must
	// still surface its message.
	err := decodeAPIError(404, []byte("404 page not found\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404 page not found")
}

func TestWorkflowErrorHintByStatus(t *testing.T) {
	t.Parallel()

	// 503 with the server's canonical "no proxy advertises" detail → the engine
	// is not enabled on any proxy; point at the proxy-config.yaml workflow block.
	err503 := workflowError(503, []byte(
		`{"detail":"workflow engine is not available: no configured proxy advertises it"}`))
	assert.Contains(t, err503.Error(), "not enabled on any configured proxy")
	assert.Contains(t, err503.Error(), "proxy-config.yaml")

	// A forwarded upstream 503 (no panda short-circuit phrase) is an availability
	// problem, not a local misconfig — it must not claim the engine is unenabled.
	err503upstream := workflowError(503, []byte("<html><body>503 Service Temporarily Unavailable</body></html>"))
	assert.Contains(t, err503upstream.Error(), "server error upstream")
	assert.NotContains(t, err503upstream.Error(), "not enabled on any configured proxy")

	// 502 splits by hop: the server's "proxy is unreachable" vs the proxy's
	// "configured but unreachable" (engine down).
	err502proxy := workflowError(502, []byte(`{"detail":"proxy is unreachable"}`))
	assert.Contains(t, err502proxy.Error(), "could not reach the proxy")
	assert.Contains(t, err502proxy.Error(), "proxies[]")

	err502engine := workflowError(502, []byte(`{"detail":"workflow engine is configured but unreachable"}`))
	assert.Contains(t, err502engine.Error(), "could not reach the workflow engine")

	// A verbatim 502 page from beyond the proxy carries neither canonical phrase;
	// it must not blame the proxy→engine hop.
	err502upstream := workflowError(502, []byte("<html><body>502 Bad Gateway</body></html>"))
	assert.Contains(t, err502upstream.Error(), "server error upstream")
	assert.NotContains(t, err502upstream.Error(), "could not reach the workflow engine")

	// Other upstream 5xx get a generic upstream-error hint rather than none.
	err500 := workflowError(500, []byte(`{"detail":"boom"}`))
	assert.Contains(t, err500.Error(), "server error upstream")
	err504 := workflowError(504, []byte("504 Gateway Time-out"))
	assert.Contains(t, err504.Error(), "server error upstream")

	// 401 picks proxy vs upstream wording by body origin: a proxy problem+json
	// body mentions the proxy; an engine body relayed verbatim does not.
	err401proxy := workflowError(401, []byte(`{"detail":"the proxy rejected the bearer token"}`))
	assert.Contains(t, err401proxy.Error(), "proxy rejected your credential")
	assert.Contains(t, err401proxy.Error(), "panda auth login")

	err401upstream := workflowError(401, []byte(`{"error":"invalid api key"}`))
	assert.Contains(t, err401upstream.Error(), "workflow engine rejected the credential")
	assert.Contains(t, err401upstream.Error(), "passthrough auth")

	// 403 → the caller's org is not allowed on this proxy.
	err403 := workflowError(403, []byte(`{"detail":"organization not allowed"}`))
	assert.Contains(t, err403.Error(), "not allowed to use the workflow engine")
	assert.Contains(t, err403.Error(), "ask an operator")

	err404 := workflowError(404, []byte("404 page not found"))
	assert.Contains(t, err404.Error(), "check the whiteboard")
	assert.Contains(t, err404.Error(), "panda workflow api")

	// A status without a workflow hint still renders the decoded message.
	err409 := workflowError(409, []byte(`{"detail":"conflict"}`))
	assert.Contains(t, err409.Error(), "conflict")
}

func TestReadInlineOrFile(t *testing.T) {
	t.Parallel()

	inline, err := readInlineOrFile(`{"a":1}`)
	require.NoError(t, err)
	assert.JSONEq(t, `{"a":1}`, string(inline))

	empty, err := readInlineOrFile("")
	require.NoError(t, err)
	assert.Nil(t, empty)

	path := t.TempDir() + "/body.json"
	require.NoError(t, os.WriteFile(path, []byte(`{"b":2}`), 0o600))

	fromFile, err := readInlineOrFile("@" + path)
	require.NoError(t, err)
	assert.JSONEq(t, `{"b":2}`, string(fromFile))

	_, err = readInlineOrFile("@/nonexistent/path.json")
	require.Error(t, err)
}

func TestReadJSONFlagValidatesPayload(t *testing.T) {
	t.Parallel()

	inline, err := readJSONFlag("--inputs", `{"a":1}`)
	require.NoError(t, err)
	assert.JSONEq(t, `{"a":1}`, string(inline))

	empty, err := readJSONFlag("--inputs", "")
	require.NoError(t, err)
	assert.Nil(t, empty)

	// Invalid JSON must fail with the flag name, not a RawMessage marshal error.
	_, err = readJSONFlag("--inputs", `{oops`)
	require.ErrorContains(t, err, "--inputs is not valid JSON")

	path := t.TempDir() + "/bad.json"
	require.NoError(t, os.WriteFile(path, []byte(`not json`), 0o600))

	_, err = readJSONFlag("--dispatch", "@"+path)
	require.ErrorContains(t, err, "--dispatch is not valid JSON")
}

func TestBuildRunBodyRejectsInvalidJSON(t *testing.T) {
	t.Parallel()

	_, err := buildRunBody(`{oops`, "")
	require.ErrorContains(t, err, "--inputs is not valid JSON")

	_, err = buildRunBody("", `[unclosed`)
	require.ErrorContains(t, err, "--dispatch is not valid JSON")
}

func TestWorkflowStreamCarriesEnvAttribution(t *testing.T) {
	// No t.Parallel(): t.Setenv and the shared cfgFile global forbid it.
	var gotAttribution, gotAccept string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAttribution = r.Header.Get(attribution.Header)
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {}\n\n"))
	}))
	defer server.Close()

	setClientConfig(t, server.URL)
	t.Setenv(attribution.EnvVar, "agent:claude")

	resp, err := workflowStream(context.Background(), http.MethodGet, nil, sseHeaders(), nil,
		workflowPath("whiteboards", "wb_1", "state", "stream"))
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)

	assert.Equal(t, "agent:claude", gotAttribution,
		"streaming requests must carry the on-behalf-of header like serverDo does")
	assert.Equal(t, "text/event-stream", gotAccept)
}

func TestParseSSESkipsCommentAndSync(t *testing.T) {
	t.Parallel()

	stream := strings.Join([]string{
		": ping",
		"",
		"event: sync",
		"id: 5",
		"data: {\"replay\":true}",
		"",
		"event: run.updated",
		"id: 6",
		"data: {\"status\":\"running\"}",
		"",
		": ping",
		"",
		"event: turn.completed",
		"data: {\"done\":true}",
		"",
	}, "\n")

	var got []sseEvent

	err := parseSSE(context.Background(), strings.NewReader(stream), func(ev sseEvent) error {
		got = append(got, ev)

		return nil
	})
	require.NoError(t, err)

	require.Len(t, got, 2, "sync and comment frames must be dropped")
	assert.Equal(t, "run.updated", got[0].Event)
	assert.Equal(t, "6", got[0].ID)
	assert.JSONEq(t, `{"status":"running"}`, got[0].Data)
	assert.Equal(t, "turn.completed", got[1].Event)
}

func TestParseSSEMultiLineData(t *testing.T) {
	t.Parallel()

	stream := "event: msg\ndata: line1\ndata: line2\n\n"

	var got []sseEvent

	err := parseSSE(context.Background(), strings.NewReader(stream), func(ev sseEvent) error {
		got = append(got, ev)

		return nil
	})
	require.NoError(t, err)

	require.Len(t, got, 1)
	assert.Equal(t, "line1\nline2", got[0].Data)
}

// fragmentReader returns its payload one byte at a time to exercise reads that
// split mid-frame.
type fragmentReader struct {
	data []byte
	pos  int
}

func (f *fragmentReader) Read(p []byte) (int, error) {
	if f.pos >= len(f.data) {
		return 0, io.EOF
	}

	p[0] = f.data[f.pos]
	f.pos++

	return 1, nil
}

func TestParseSSEFragmentedReads(t *testing.T) {
	t.Parallel()

	stream := "event: run.updated\ndata: {\"a\":1}\n\nevent: turn.completed\ndata: {\"b\":2}\n\n"

	var got []sseEvent

	reader := &fragmentReader{data: []byte(stream)}

	err := parseSSE(context.Background(), reader, func(ev sseEvent) error {
		got = append(got, ev)

		return nil
	})
	require.NoError(t, err)

	require.Len(t, got, 2)
	assert.JSONEq(t, `{"a":1}`, got[0].Data)
	assert.JSONEq(t, `{"b":2}`, got[1].Data)
}

func TestParseSSELargeEventOver64K(t *testing.T) {
	t.Parallel()

	// bufio.Scanner's default token cap is 64K; the parser must handle a larger
	// single data payload.
	big := strings.Repeat("x", 200*1024)
	stream := "event: msg\ndata: " + big + "\n\n"

	var got []sseEvent

	err := parseSSE(context.Background(), strings.NewReader(stream), func(ev sseEvent) error {
		got = append(got, ev)

		return nil
	})
	require.NoError(t, err)

	require.Len(t, got, 1)
	assert.Len(t, got[0].Data, len(big))
}

func TestParseSSERejectsOversizeLine(t *testing.T) {
	t.Parallel()

	// A single newline-sparse line past the 8 MiB ceiling (e.g. a large base64
	// artifact mis-routed through `api -f`) must return a bounded error rather
	// than buffer unbounded.
	huge := strings.Repeat("x", maxSSELineBytes+1024)
	stream := "data: " + huge + "\n\n"

	var got []sseEvent

	err := parseSSE(context.Background(), strings.NewReader(stream), func(ev sseEvent) error {
		got = append(got, ev)

		return nil
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
	assert.Empty(t, got, "no frame should be emitted for an oversize line")
}

func TestParseSSEContextCancel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := parseSSE(ctx, strings.NewReader("data: x\n\n"), func(sseEvent) error {
		return nil
	})
	require.ErrorIs(t, err, context.Canceled)
}

func TestWorkerLogTerminalType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data string
		want bool
	}{
		{
			name: "page with a terminal turn.completed item",
			data: `{"items":[{"type":"worker.system","eventName":"boot"},` +
				`{"type":"turn.completed","eventName":"done"}],"liveCursor":"c2"}`,
			want: true,
		},
		{
			name: "page with a terminal worker.operation.completed item",
			data: `{"items":[{"type":"worker.operation.completed"}],"liveCursor":"c3"}`,
			want: true,
		},
		{
			name: "page with only non-terminal items",
			data: `{"items":[{"type":"worker.system"},` +
				`{"type":"worker.operation.started"}],"liveCursor":"c1"}`,
			want: false,
		},
		{
			name: "empty page",
			data: `{"items":[],"liveCursor":"c0"}`,
			want: false,
		},
		{
			name: "empty payload",
			data: "",
			want: false,
		},
		{
			name: "non-page object (state snapshot) is never terminal",
			data: `{"run":{"status":"completed"}}`,
			want: false,
		},
		{
			name: "malformed json",
			data: "not json",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, workerLogTerminalType([]byte(tt.data)))
		})
	}
}

func TestEmitStreamItemsFlattensPageJSON(t *testing.T) {
	restore := outputFormat
	outputFormat = "json"
	defer func() { outputFormat = restore }()

	page := `{"items":[{"type":"worker.system","message":"a"},` +
		`{"type":"turn.completed","message":"b"}],"liveCursor":"c1"}`

	out := captureStdout(t, func() {
		require.NoError(t, emitStreamItems(sseEvent{Event: "page", Data: page}))
	})

	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.Len(t, lines, 2, "each items[] element must be its own NDJSON line")

	var first map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	assert.Equal(t, "worker.system", first["type"])

	var second map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &second))
	assert.Equal(t, "turn.completed", second["type"])
}

func TestEmitStreamItemsPassesThroughNonPage(t *testing.T) {
	restore := outputFormat
	outputFormat = "json"
	defer func() { outputFormat = restore }()

	snapshot := `{"run":{"status":"completed"}}`

	out := captureStdout(t, func() {
		require.NoError(t, emitStreamItems(sseEvent{Event: "state.updated", Data: snapshot}))
	})

	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.Len(t, lines, 1, "a non-page snapshot passes through as one line")
	assert.JSONEq(t, snapshot, lines[0])
}

func TestSummarizeWorkerLogRendersTypeMessage(t *testing.T) {
	restore := outputFormat
	outputFormat = ""
	defer func() { outputFormat = restore }()

	body := `{"items":[` +
		`{"type":"worker.system","message":"boot"},` +
		`{"type":"turn.completed","message":"done"}],"liveCursor":"c1"}`

	out := captureStdout(t, func() {
		summarizeWorkerLog([]byte(body), "No log entries.")
	})

	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.Len(t, lines, 2, "one line per worker-log event, not a dash table")
	assert.Equal(t, "worker.system\tboot", lines[0])
	assert.Equal(t, "turn.completed\tdone", lines[1])
	assert.NotContains(t, out, "-\t-\t-", "must not render the id/name/status dash table")
}

func TestSummarizeWorkerLogEmpty(t *testing.T) {
	restore := outputFormat
	outputFormat = ""
	defer func() { outputFormat = restore }()

	out := captureStdout(t, func() {
		summarizeWorkerLog([]byte(`{"items":[],"liveCursor":"c0"}`), "No log entries.")
	})

	assert.Equal(t, "No log entries.", strings.TrimSpace(out))
}

func TestSummarizeWorkflowItemsFallsBackForIDlessLists(t *testing.T) {
	restore := outputFormat
	outputFormat = ""
	defer func() { outputFormat = restore }()

	// A steer/session turn carries turnId/taskKey but no id/name/status, plus a
	// large nested events[] the summary must NOT dump. A dash table would be
	// useless, so it falls back to a compact scalar line (events[] excluded).
	body := `{"items":[{"turnId":"worker-turn:wop_1","turnSeq":1,` +
		`"taskKey":"tasks.fetch[iter=0000]","events":[{"big":"payload"}]}]}`

	out := captureStdout(t, func() {
		summarizeWorkflowItems([]byte(body), "items", "No turns.")
	})

	trimmed := strings.TrimSpace(out)
	assert.Contains(t, trimmed, "turnId=worker-turn:wop_1")
	assert.Contains(t, trimmed, "taskKey=tasks.fetch[iter=0000]")
	assert.Contains(t, trimmed, "turnSeq=1")
	assert.NotContains(t, out, "ID  NAME  STATUS", "no dash table for id-less items")
	assert.NotContains(t, out, "events=", "nested events[] must be excluded from the scalar line")
	assert.NotContains(t, out, "big", "nested payload must not leak into the summary")
}

func TestSummarizeWorkflowItemsKeepsTableWhenIdentified(t *testing.T) {
	restore := outputFormat
	outputFormat = ""
	defer func() { outputFormat = restore }()

	// A list whose items carry id/name/status still renders the table.
	body := `{"items":[{"id":"wf_1","name":"calc","status":"active"}]}`

	out := captureStdout(t, func() {
		summarizeWorkflowItems([]byte(body), "items", "No workflows.")
	})

	assert.Contains(t, out, "ID")
	assert.Contains(t, out, "wf_1")
	assert.Contains(t, out, "calc")
	assert.Contains(t, out, "active")
}

func TestScalarFieldLineSkipsNestedAndNil(t *testing.T) {
	t.Parallel()

	line := scalarFieldLine(map[string]any{
		"agent":          "claude",
		"model":          "sonnet",
		"workerCount":    float64(2),
		"availableSlots": nil,
		"nested":         map[string]any{"x": 1},
		"list":           []any{1, 2},
	})

	assert.Contains(t, line, "agent=claude")
	assert.Contains(t, line, "model=sonnet")
	assert.Contains(t, line, "workerCount=2")
	assert.NotContains(t, line, "availableSlots", "nil values are skipped")
	assert.NotContains(t, line, "nested", "nested objects are skipped")
	assert.NotContains(t, line, "list", "nested arrays are skipped")
}

func TestRunStatusFromState(t *testing.T) {
	t.Parallel()

	snapshot, err := json.Marshal(map[string]any{
		"run": map[string]any{"status": "completed"},
	})
	require.NoError(t, err)

	assert.Equal(t, "completed", runStatusFromState(snapshot))
	assert.Equal(t, "", runStatusFromState([]byte(`{"run":{}}`)))
	assert.Equal(t, "", runStatusFromState([]byte("not json")))
}
