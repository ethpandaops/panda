package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// workflowRunFollowCmd is the background-friendly sibling of `run watch`. watch
// streams a full state snapshot per event — right for a foreground tail, far
// too verbose for a background task whose accumulated output gets read later;
// follow inverts the contract: change-only progress lines on stderr, exactly
// one final JSON summary on stdout, and the run-status exit code.
var workflowRunFollowCmd = &cobra.Command{
	Use:   "follow <wf> <run>",
	Short: "Follow a run to terminal: delta progress on stderr, one summary JSON on stdout",
	Long: `Follow a run until terminal status, built for running as a background
task. Progress goes to stderr as one short line per change (task/run status
transitions, failures); stdout carries exactly one JSON summary object at
terminal — status, duration, task counts, failed-task errors, scalar outputs,
and artifact resources — so reading the output afterwards is cheap. A dropped
stream reconnects automatically (the run may finish during the gap; that is
caught on reconnect).

The run-stream exit contract applies: 0 on completed, non-zero on
failed/cancelled. Ctrl-C detaches cleanly without a summary; the run keeps
going. --json is a no-op here — stdout is always the JSON summary.

Foreground alternatives: 'run watch' (full snapshot stream), 'run logs -f'
(worker-log tail).`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return followWorkflowRun(commandContext(cmd), args[0], args[1])
	},
}

// followReconnects bounds CONSECUTIVE fruitless stream reconnect attempts: a
// stream that delivered at least one event resets the budget, so a long run
// behind an idle-timeouting load balancer is not bounded to 10 drops over its
// lifetime. Each reconnect refetches the state snapshot first, so a run that
// finished while the stream was down terminates immediately instead of burning
// an attempt waiting.
const followReconnects = 10

// followReconnectGap is the pause between stream reconnect attempts.
const followReconnectGap = 3 * time.Second

// followTaskError caps a failed task's error text in progress lines and the
// summary, so one giant upstream error cannot bloat either.
const followTaskErrorCap = 300

// followView is the diffable digest of one run /state snapshot.
type followView struct {
	initialized bool
	runStatus   string
	order       []string          // task ids in upstream order
	status      map[string]string // task id → status
	errs        map[string]string // task id → compact error (failed tasks)
}

// followArtifact is one artifact resource row in the final summary.
type followArtifact struct {
	ID        string `json:"id"`
	SlotName  string `json:"slotName,omitempty"`
	MediaType string `json:"mediaType,omitempty"`
	SizeBytes int64  `json:"sizeBytes,omitempty"`
	TaskKey   string `json:"taskKey,omitempty"`
}

// followFailure names a failed task and its (capped) error in the summary.
type followFailure struct {
	ID    string `json:"id"`
	Error string `json:"error,omitempty"`
}

// runFollowSummary is the single stdout object `run follow` emits at terminal.
type runFollowSummary struct {
	WorkflowID      string           `json:"workflowId"`
	RunID           string           `json:"runId"`
	Status          string           `json:"status"`
	DurationSeconds float64          `json:"durationSeconds,omitempty"`
	TasksTotal      int              `json:"tasksTotal"`
	TasksByStatus   map[string]int   `json:"tasksByStatus,omitempty"`
	FailedTasks     []followFailure  `json:"failedTasks,omitempty"`
	Outputs         map[string]any   `json:"outputs,omitempty"`
	Artifacts       []followArtifact `json:"artifacts,omitempty"`
	// Links carries frontend URLs (run, workflow) when the server exposes
	// workflow's web origin; users log in there themselves.
	Links map[string]string `json:"links,omitempty"`
}

// followStatePayload is the decoded slice of a run /state snapshot that follow
// renders. Task identity prefers specNodeKey (the steer/log key) over id.
type followStatePayload struct {
	Run struct {
		ID         string         `json:"id"`
		Status     string         `json:"status"`
		Outputs    map[string]any `json:"outputs"`
		StartedAt  string         `json:"startedAt"`
		FinishedAt string         `json:"finishedAt"`
	} `json:"run"`
	Tasks []struct {
		ID          string `json:"id"`
		SpecNodeKey string `json:"specNodeKey"`
		TaskKey     string `json:"taskKey"`
		Status      string `json:"status"`
		Error       any    `json:"error"`
	} `json:"tasks"`
	Resources []struct {
		ID        string `json:"id"`
		SlotName  string `json:"slotName"`
		MediaType string `json:"mediaType"`
		SizeBytes int64  `json:"sizeBytes"`
		TaskKey   string `json:"taskKey"`
	} `json:"resources"`
}

// followWorkflowRun drives the follow loop: snapshot → delta → stream → refetch
// per event → delta, reconnecting on stream drops, until terminal run.status.
func followWorkflowRun(ctx context.Context, wf, run string) error {
	statePath := []string{"workflows", wf, "runs", run, "state"}
	streamPath := workflowPath("workflows", wf, "runs", run, "state", "stream")

	// Resolve the frontend origin once, best-effort: the summary carries
	// clickable links when available and stays link-less otherwise.
	webBase := workflowWebBaseBestEffort(ctx)

	prev := followView{}
	fruitless := 0

	for {
		snap, err := workflowGet(ctx, nil, statePath...)
		if err != nil {
			return err
		}

		view := parseFollowView(snap)
		printFollowDelta(os.Stderr, run, prev, view)
		prev = view

		if isTerminalRunStatus(view.runStatus) {
			return emitFollowSummary(os.Stdout, snap, wf, run, webBase)
		}

		termErr, streamErr, delivered := followStreamOnce(ctx, statePath, streamPath, &prev, wf, run, webBase)
		if streamErr == nil || errors.Is(streamErr, errStreamComplete) {
			return resolveStreamResult(ctx, streamErr, termErr)
		}

		if ctx.Err() != nil {
			fmt.Fprintf(os.Stderr, "detached from run %s (still active)\n", run)

			return nil
		}

		// The budget bounds consecutive fruitless attempts, not lifetime drops:
		// a stream that made progress before dropping resets it.
		if delivered {
			fruitless = 0
		} else {
			fruitless++
		}

		if fruitless >= followReconnects {
			return fmt.Errorf("stream failed after %d consecutive reconnects: %w", followReconnects, streamErr)
		}

		fmt.Fprintf(os.Stderr, "stream dropped (%v) — reconnecting\n", streamErr)

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(followReconnectGap):
		}
	}
}

// followStreamOnce opens the state stream and refetches + diffs the snapshot
// per event until the run is terminal (summary emitted, errStreamComplete) or
// the stream drops (streamErr for the caller's reconnect loop). delivered
// reports whether the stream yielded at least one event before dropping, so
// the caller can reset its consecutive-reconnect budget.
func followStreamOnce(
	ctx context.Context,
	statePath []string,
	streamPath string,
	prev *followView,
	wf, run, webBase string,
) (termErr, streamErr error, delivered bool) {
	resp, err := workflowStream(ctx, "GET", nil, sseHeaders(), url.Values{}, streamPath)
	if err != nil {
		return nil, err, false
	}
	defer func() { _ = resp.Body.Close() }()

	streamErr = parseSSE(ctx, resp.Body, func(_ sseEvent) error {
		delivered = true

		snap, refetchErr := workflowGet(ctx, nil, statePath...)
		if refetchErr != nil {
			return refetchErr
		}

		view := parseFollowView(snap)
		printFollowDelta(os.Stderr, run, *prev, view)
		*prev = view

		if isTerminalRunStatus(view.runStatus) {
			termErr = emitFollowSummary(os.Stdout, snap, wf, run, webBase)

			return errStreamComplete
		}

		return nil
	})

	// A clean EOF before terminal is a drop for follow purposes: the caller
	// must reconnect rather than exit without a summary.
	if streamErr == nil {
		streamErr = errors.New("stream closed before terminal status")
	}

	return termErr, streamErr, delivered
}

// parseFollowView digests a /state snapshot into the diffable view.
func parseFollowView(snapshot []byte) followView {
	var payload followStatePayload

	view := followView{
		initialized: true,
		status:      map[string]string{},
		errs:        map[string]string{},
	}

	if err := json.Unmarshal(snapshot, &payload); err != nil {
		return view
	}

	view.runStatus = payload.Run.Status

	for _, task := range payload.Tasks {
		id := task.SpecNodeKey
		if id == "" {
			id = task.ID
		}

		if id == "" {
			id = task.TaskKey
		}

		if id == "" {
			continue
		}

		view.order = append(view.order, id)
		view.status[id] = task.Status

		if msg := compactFollowError(task.Error); msg != "" {
			view.errs[id] = msg
		}
	}

	return view
}

// compactFollowError renders a task error (string or structured) as a single
// capped line, or "" when absent.
func compactFollowError(v any) string {
	var msg string

	switch t := v.(type) {
	case nil:
		return ""
	case string:
		msg = t
	default:
		data, err := json.Marshal(t)
		if err != nil {
			return ""
		}

		msg = string(data)
	}

	msg = strings.Join(strings.Fields(msg), " ")
	if len(msg) > followTaskErrorCap {
		msg = msg[:followTaskErrorCap] + "…"
	}

	return msg
}

// printFollowDelta writes change-only progress lines: an attach line on the
// first snapshot, then one line per run-status change, per new task, and per
// task transition (with the error for failures). An unchanged snapshot prints
// nothing.
func printFollowDelta(w io.Writer, run string, prev, view followView) {
	if !prev.initialized {
		_, _ = fmt.Fprintf(w, "following run %s  status: %s  tasks: %d\n",
			run, orDash(view.runStatus), len(view.order))

		for _, id := range view.order {
			_, _ = fmt.Fprintf(w, "  %s  %s\n", id, orDash(view.status[id]))
		}

		return
	}

	if view.runStatus != prev.runStatus {
		_, _ = fmt.Fprintf(w, "run  %s → %s\n", orDash(prev.runStatus), orDash(view.runStatus))
	}

	for _, id := range view.order {
		current := view.status[id]

		before, known := prev.status[id]
		if known && before == current {
			continue
		}

		switch {
		case !known:
			_, _ = fmt.Fprintf(w, "task %s  → %s\n", id, orDash(current))
		default:
			_, _ = fmt.Fprintf(w, "task %s  %s → %s\n", id, orDash(before), orDash(current))
		}

		if msg, failed := view.errs[id]; failed && msg != prev.errs[id] {
			_, _ = fmt.Fprintf(w, "  error: %s\n", msg)
		}
	}
}

// orDash substitutes "-" for an empty status so delta lines stay readable.
func orDash(s string) string {
	if s == "" {
		return "-"
	}

	return s
}

// emitFollowSummary builds and prints the single stdout summary from the
// terminal snapshot, then returns the run-status exit error (nil on completed).
func emitFollowSummary(w io.Writer, snapshot []byte, wf, run, webBase string) error {
	summary := buildFollowSummary(snapshot, wf, run, webBase)

	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return fmt.Errorf("building summary: %w", err)
	}

	_, _ = fmt.Fprintln(w, string(data))

	return runStatusExitError(summary.Status)
}

// buildFollowSummary digests the terminal /state snapshot into the summary
// object. It never fails: an undecodable snapshot yields a summary with only
// the ids, and the caller's exit code falls back to non-zero via the empty
// status.
func buildFollowSummary(snapshot []byte, wf, run, webBase string) *runFollowSummary {
	summary := &runFollowSummary{WorkflowID: wf, RunID: run}

	if webBase != "" {
		summary.Links = map[string]string{
			"run":      workflowRunURL(webBase, wf, run),
			"workflow": workflowWorkflowURL(webBase, wf),
		}
	}

	var payload followStatePayload
	if err := json.Unmarshal(snapshot, &payload); err != nil {
		return summary
	}

	summary.Status = payload.Run.Status
	summary.Outputs = payload.Run.Outputs
	summary.TasksTotal = len(payload.Tasks)
	summary.DurationSeconds = followDurationSeconds(payload.Run.StartedAt, payload.Run.FinishedAt)

	if len(payload.Tasks) > 0 {
		summary.TasksByStatus = map[string]int{}
	}

	for _, task := range payload.Tasks {
		summary.TasksByStatus[orDash(task.Status)]++

		if task.Status != "failed" {
			continue
		}

		id := task.SpecNodeKey
		if id == "" {
			id = task.ID
		}

		summary.FailedTasks = append(summary.FailedTasks, followFailure{
			ID:    id,
			Error: compactFollowError(task.Error),
		})
	}

	for _, res := range payload.Resources {
		summary.Artifacts = append(summary.Artifacts, followArtifact{
			ID:        res.ID,
			SlotName:  res.SlotName,
			MediaType: res.MediaType,
			SizeBytes: res.SizeBytes,
			TaskKey:   res.TaskKey,
		})
	}

	return summary
}

// followDurationSeconds derives the run duration from its RFC3339 timestamps,
// or 0 when either is absent/invalid.
func followDurationSeconds(startedAt, finishedAt string) float64 {
	start, err := time.Parse(time.RFC3339, startedAt)
	if err != nil {
		return 0
	}

	end, err := time.Parse(time.RFC3339, finishedAt)
	if err != nil {
		return 0
	}

	if seconds := end.Sub(start).Seconds(); seconds > 0 {
		return seconds
	}

	return 0
}
