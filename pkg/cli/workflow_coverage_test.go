package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sseFlush writes an SSE frame to w and flushes it so the client parser sees it
// before the handler blocks or returns.
func sseFlush(t *testing.T, w http.ResponseWriter, frame string) {
	t.Helper()

	_, err := io.WriteString(w, frame)
	require.NoError(t, err)

	flusher, ok := w.(http.Flusher)
	require.True(t, ok, "ResponseWriter must support Flush for SSE")
	flusher.Flush()
}

// G1: `run cancel` must accept a 204 with an empty body (no JSON parse) and print
// the `run <id> cancelled` confirmation in text mode.
func TestRunCancelHandles204EmptyBody(t *testing.T) {
	var gotMethod, gotPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent) // 204, empty body — must not be parsed.
	}))
	defer server.Close()

	setClientConfig(t, server.URL)
	setOutputFormat(t, "text")

	cmd := testCommand()

	var out bytes.Buffer

	cmd.SetOut(&out)

	require.NotPanics(t, func() {
		require.NoError(t, workflowRunCancelCmd.RunE(cmd, []string{"wf_1", "run_1"}))
	})

	assert.Equal(t, "run run_1 cancelled\n", out.String())
	assert.Equal(t, http.MethodPost, gotMethod)
	assert.Equal(t, "/api/v1/workflow/workflows/wf_1/runs/run_1/cancel", gotPath)
}

// G2: isTerminalRunStatus classifies the terminal run.status set.
func TestIsTerminalRunStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status string
		want   bool
	}{
		{"completed", true},
		{"failed", true},
		{"cancelled", true},
		{"running", false},
		{"pending", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, isTerminalRunStatus(tt.status))
		})
	}
}

// G2: runSnapshotTerminal derives (done, exitErr) from a run /state snapshot —
// the run-watch terminal derivation. Non-terminal/absent status is never done;
// completed is done with exit 0; failed/cancelled are done with exit code 1.
func TestRunSnapshotTerminal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		snapshot string
		wantDone bool
		wantCode int // -1 = expect nil error
	}{
		{"completed exits 0", `{"run":{"status":"completed"}}`, true, -1},
		{"failed exits 1", `{"run":{"status":"failed"}}`, true, 1},
		{"cancelled exits 1", `{"run":{"status":"cancelled"}}`, true, 1},
		{"running not terminal", `{"run":{"status":"running"}}`, false, -1},
		{"absent status not terminal", `{"run":{}}`, false, -1},
		{"malformed not terminal", "not json", false, -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			done, err := runSnapshotTerminal([]byte(tt.snapshot))
			assert.Equal(t, tt.wantDone, done)

			if tt.wantCode < 0 {
				assert.NoError(t, err)

				return
			}

			var exitErr *exitCodeError
			require.ErrorAs(t, err, &exitErr)
			assert.Equal(t, tt.wantCode, exitErr.code)
		})
	}
}

// G2: followRunLogs streams the worker log while polling /state out-of-band; when
// the poll observes a terminal status it cancels the stream and exits with the
// status-derived code (0 on completed, non-zero on failed/cancelled).
func TestFollowRunLogsExitsOnPolledTerminalStatus(t *testing.T) {
	tests := []struct {
		name     string
		status   string
		wantCode int // -1 = nil (exit 0)
	}{
		{"completed exits 0", "completed", -1},
		{"failed exits non-zero", "failed", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restore := workflowRunLogsPollGap
			workflowRunLogsPollGap = 5 * time.Millisecond
			defer func() { workflowRunLogsPollGap = restore }()

			release := make(chan struct{})

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/worker-log/stream") {
					w.Header().Set("Content-Type", "text/event-stream")
					sseFlush(t, w, "event: page\ndata: {\"items\":[{\"type\":\"worker.system\"}],"+
						"\"liveCursor\":\"c1\"}\n\n")
					<-release // Keep the stream open until the poll cancels it.

					return
				}

				// /state poll returns a terminal status immediately.
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"run":{"status":"`+tt.status+`"}}`)
			}))
			defer server.Close()
			defer close(release) // LIFO: released before server.Close so the handler unblocks.

			setClientConfig(t, server.URL)
			setOutputFormat(t, "json")

			var err error

			_ = captureStdout(t, func() {
				err = followRunLogs(context.Background(), "wf_1", "run_1", "", nil, nil)
			})

			if tt.wantCode < 0 {
				assert.NoError(t, err)

				return
			}

			var exitErr *exitCodeError
			require.ErrorAs(t, err, &exitErr)
			assert.Equal(t, tt.wantCode, exitErr.code)
		})
	}
}

// L1: when the worker-log stream ends before the out-of-band /state poll
// observed a terminal status, followRunLogs must do one final synchronous
// /state read so a failed run still exits non-zero. Here the poll only ever
// sees "running"; the stream then EOFs and the final fetch reports "failed".
func TestFollowRunLogsFinalStateFetchSetsExitCode(t *testing.T) {
	restore := workflowRunLogsPollGap
	workflowRunLogsPollGap = time.Hour // only the immediate first poll fires
	defer func() { workflowRunLogsPollGap = restore }()

	release := make(chan struct{})
	firstPoll := make(chan struct{})

	var stateCalls int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/worker-log/stream") {
			w.Header().Set("Content-Type", "text/event-stream")
			sseFlush(t, w, "event: page\ndata: {\"items\":[{\"type\":\"worker.system\"}],"+
				"\"liveCursor\":\"c1\"}\n\n")
			<-release // hold open until the poll has observed a non-terminal status

			return
		}

		// /state: the first (poll) read is non-terminal; the later final read is
		// terminal, so only the final fetch can drive the exit code.
		w.Header().Set("Content-Type", "application/json")

		if atomic.AddInt32(&stateCalls, 1) == 1 {
			_, _ = io.WriteString(w, `{"run":{"status":"running"}}`)
			close(firstPoll)

			return
		}

		_, _ = io.WriteString(w, `{"run":{"status":"failed"}}`)
	}))
	defer server.Close()

	setClientConfig(t, server.URL)
	setOutputFormat(t, "json")

	// Release the held-open stream only after the poll has read "running", so the
	// poll never sees a terminal status and the final /state fetch is the only
	// thing that can set a non-zero exit code.
	go func() {
		select {
		case <-firstPoll:
		case <-time.After(5 * time.Second):
		}

		close(release)
	}()

	var err error

	_ = captureStdout(t, func() {
		err = followRunLogs(context.Background(), "wf_1", "run_1", "", nil, nil)
	})

	var exitErr *exitCodeError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 1, exitErr.code)
}

// G3: `artifact get --out FILE` writes the raw bytes to the file; `--out ""`
// streams them to stdout.
func TestArtifactGetOutFileAndStdout(t *testing.T) {
	payload := "artifact bytes\x00\x01binary"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/workflow/workflows/wf_1/runs/run_1/artifacts/art_1/content", r.URL.Path)
		_, _ = io.WriteString(w, payload)
	}))
	defer server.Close()

	setClientConfig(t, server.URL)
	setOutputFormat(t, "text")

	restore := workflowArtifactOut
	defer func() { workflowArtifactOut = restore }()

	t.Run("out file", func(t *testing.T) {
		outPath := filepath.Join(t.TempDir(), "report.md")
		workflowArtifactOut = outPath

		require.NoError(t, workflowArtifactGetCmd.RunE(testCommand(), []string{"wf_1", "run_1", "art_1"}))

		data, err := os.ReadFile(outPath)
		require.NoError(t, err)
		assert.Equal(t, payload, string(data))
	})

	t.Run("stdout", func(t *testing.T) {
		workflowArtifactOut = ""

		out := captureStdout(t, func() {
			require.NoError(t, workflowArtifactGetCmd.RunE(testCommand(), []string{"wf_1", "run_1", "art_1"}))
		})

		assert.Equal(t, payload, out)
	})
}

// G4: `session logs -f` exits 0 on the FIRST terminal item, without waiting for
// end-of-stream. The server holds the connection open after the terminal frame;
// followSessionLogs must still return promptly (via the stream-complete sentinel).
func TestFollowSessionLogsExitsOnFirstTerminalItem(t *testing.T) {
	release := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		sseFlush(t, w, "event: page\ndata: {\"items\":[{\"type\":\"turn.completed\"}],"+
			"\"liveCursor\":\"c1\"}\n\n")
		<-release // Stays open; the command must not wait for this to exit.
	}))
	defer server.Close()
	defer close(release)

	setClientConfig(t, server.URL)
	setOutputFormat(t, "json")

	var err error

	out := captureStdout(t, func() {
		err = followSessionLogs(context.Background(), "wb_1", "sid_1", "")
	})

	require.NoError(t, err, "a terminal item must exit 0")
	assert.Contains(t, out, "turn.completed")
}

// G4: a non-terminal page must NOT end the follow — the stream keeps consuming
// items until a terminal item (or EOF) is reached.
func TestFollowSessionLogsContinuesPastNonTerminalItem(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// A non-terminal page first, then a terminal page. If the non-terminal
		// page ended the follow, only the first item would be emitted.
		sseFlush(t, w, "event: page\ndata: {\"items\":[{\"type\":\"worker.system\"}],"+
			"\"liveCursor\":\"c1\"}\n\n")
		sseFlush(t, w, "event: page\ndata: {\"items\":[{\"type\":\"turn.completed\"}],"+
			"\"liveCursor\":\"c2\"}\n\n")
	}))
	defer server.Close()

	setClientConfig(t, server.URL)
	setOutputFormat(t, "json")

	var err error

	out := captureStdout(t, func() {
		err = followSessionLogs(context.Background(), "wb_1", "sid_1", "")
	})

	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.Len(t, lines, 2, "both the non-terminal and terminal items must be emitted")
	assert.Contains(t, lines[0], "worker.system")
	assert.Contains(t, lines[1], "turn.completed")
}

// G5: `watch` follows the snapshot policy — it emits the initial /state snapshot,
// then refetches and emits the /state snapshot per stream event, as NDJSON of
// snapshots. The raw stream delta is never emitted.
func TestWatchWorkflowStateEmitsSnapshotsNotDelta(t *testing.T) {
	const snapshot = `{"whiteboard":{"id":"wb_1"},"cursor":7}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/state/stream"):
			w.Header().Set("Content-Type", "text/event-stream")
			// The stream delta carries a marker the snapshot policy must ignore.
			sseFlush(t, w, "event: state.updated\ndata: {\"delta\":\"IGNORE_ME\"}\n\n")
		case strings.HasSuffix(r.URL.Path, "/state"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, snapshot)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	setClientConfig(t, server.URL)
	setOutputFormat(t, "json")

	var err error

	out := captureStdout(t, func() {
		err = watchWorkflowState(
			context.Background(),
			0,
			[]string{"whiteboards", "wb_1", "state"},
			[]string{"whiteboards", "wb_1", "state", "stream"},
			nil,
		)
	})

	require.NoError(t, err)
	assert.NotContains(t, out, "IGNORE_ME", "the raw stream delta must never be emitted")

	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.Len(t, lines, 2, "initial snapshot + one refetched snapshot")

	for _, line := range lines {
		assert.JSONEq(t, snapshot, line, "every emitted line is a full /state snapshot")
	}
}

// G6: read/write commands guard their required flags with a clear error before
// any request is made.
func TestWorkflowRequiredFlagGuards(t *testing.T) {
	tests := []struct {
		name    string
		reset   func()
		run     func() error
		wantMsg string
	}{
		{
			name:    "steer send needs --message",
			reset:   func() { workflowSteerMessage = "" },
			run:     func() error { return workflowSteerSendCmd.RunE(testCommand(), []string{"wf", "run", "task"}) },
			wantMsg: "--message is required",
		},
		{
			name:    "session send needs --content",
			reset:   func() { workflowSessionContent = "" },
			run:     func() error { return workflowSessionSendCmd.RunE(testCommand(), []string{"wb", "sid"}) },
			wantMsg: "--content is required",
		},
		{
			name:    "dispatch simulate needs --data",
			reset:   func() { workflowDispatchData = "" },
			run:     func() error { return workflowDispatchSimulateCmd.RunE(testCommand(), nil) },
			wantMsg: "--data is required",
		},
		{
			name:    "draft revise needs --spec",
			reset:   func() { workflowDraftSpec = "" },
			run:     func() error { return workflowDraftReviseCmd.RunE(testCommand(), []string{"wb", "draft"}) },
			wantMsg: "--spec is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.reset()

			err := tt.run()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantMsg)
		})
	}
}

// Part 2: `run get` text mode surfaces the run id, status, and the scalar
// run.outputs map — the load-bearing fields that live under `.run` and were
// hidden by the previous top-level-scalar-only summary.
func TestSummarizeRunStateSurfacesStatusAndOutputs(t *testing.T) {
	setOutputFormat(t, "text")

	body := `{"cursor":243,"run":{"id":"run_1","status":"completed",` +
		`"outputs":{"sum":300,"verdict":"correct"},` +
		`"inputs":{"a":1},"dispatchPolicy":{"defaults":{"agent":"x"}}}}`

	out := captureStdout(t, func() {
		summarizeRunState([]byte(body))
	})

	assert.Contains(t, out, "run_1")
	assert.Contains(t, out, "completed")
	assert.Contains(t, out, "outputs:")
	assert.Contains(t, out, "sum")
	assert.Contains(t, out, "300")
	assert.Contains(t, out, "verdict")
	assert.Contains(t, out, "correct")
	assert.NotContains(t, out, "dispatchPolicy", "nested run fields must not dump")
	assert.NotContains(t, out, "243", "the top-level cursor is no longer the only field shown")
}

// Part 2: a body without a `.run` object falls back to the generic scalar summary
// rather than printing nothing.
func TestSummarizeRunStateFallsBackWithoutRun(t *testing.T) {
	setOutputFormat(t, "text")

	out := captureStdout(t, func() {
		summarizeRunState([]byte(`{"cursor":9}`))
	})

	assert.Contains(t, out, "cursor")
	assert.Contains(t, out, "9")
}

// Part 2: `whiteboard get` text mode surfaces the whiteboard id/name/status, the
// latestDraftId/latestSessionId pointers, and the draft/session counts.
func TestSummarizeWhiteboardStateSurfacesKeyFields(t *testing.T) {
	setOutputFormat(t, "text")

	body := `{"cursor":152,"whiteboard":{"id":"wb_1","name":"acc calc","status":"archived",` +
		`"latestDraftId":"dr_1","latestSessionId":"ses_1",` +
		`"requirements":"Calculate 7 + 8 and verify the result."},` +
		`"drafts":[{"id":"dr_1"},{"id":"dr_2"},{"id":"dr_3"}],"sessions":[{"id":"ses_1"}]}`

	out := captureStdout(t, func() {
		summarizeWhiteboardState([]byte(body))
	})

	assert.Contains(t, out, "wb_1")
	assert.Contains(t, out, "acc calc")
	assert.Contains(t, out, "archived")
	assert.Contains(t, out, "latestDraftId")
	assert.Contains(t, out, "dr_1")
	assert.Contains(t, out, "latestSessionId")
	assert.Contains(t, out, "ses_1")
	assert.Contains(t, out, "drafts")
	assert.Contains(t, out, "3")
	assert.Contains(t, out, "sessions")
	assert.Contains(t, out, "1")
}

// Part 2: a body without a `.whiteboard` object falls back to the generic scalar
// summary.
func TestSummarizeWhiteboardStateFallsBackWithoutWhiteboard(t *testing.T) {
	setOutputFormat(t, "text")

	out := captureStdout(t, func() {
		summarizeWhiteboardState([]byte(`{"cursor":5}`))
	})

	assert.Contains(t, out, "cursor")
	assert.Contains(t, out, "5")
}

// The folded `dispatch agents|workers|operations` commands must hit the same
// engine resources the old `agent list` / `worker list` / `worker operations`
// groups did, now under the `/api/v1/workflow` passthrough prefix.
func TestDispatchFoldedCommandsHitEngineEndpoints(t *testing.T) {
	tests := []struct {
		name     string
		cmd      *cobra.Command
		wantPath string
	}{
		{"agents", workflowDispatchAgentsCmd, "/api/v1/workflow/agents"},
		{"workers", workflowDispatchWorkersCmd, "/api/v1/workflow/worker-identities"},
		{"operations", workflowDispatchOperationsCmd, "/api/v1/workflow/workers/operations"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath, gotMethod string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotMethod = r.Method
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"items":[]}`)
			}))
			defer server.Close()

			setClientConfig(t, server.URL)
			setOutputFormat(t, "json")

			_ = captureStdout(t, func() {
				require.NoError(t, tt.cmd.RunE(testCommand(), nil))
			})

			assert.Equal(t, http.MethodGet, gotMethod)
			assert.Equal(t, tt.wantPath, gotPath)
		})
	}
}

// Bare `panda workflow run <wf>` must print the two-line pointer to `run create`
// and `draft run` (gh muscle memory), not cobra's unknown-command error.
func TestBareWorkflowRunPrintsPointer(t *testing.T) {
	cmd := testCommand()

	var out bytes.Buffer

	cmd.SetOut(&out)

	require.NoError(t, workflowRunCmd.RunE(cmd, []string{"wf_1"}))

	got := out.String()
	assert.Contains(t, got, "panda workflow run create wf_1")
	assert.Contains(t, got, "panda workflow draft run")
}
