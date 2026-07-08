package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/spf13/cobra"
)

var (
	workflowRunInputs      string
	workflowRunDispatch    string
	workflowRunApproved    string
	workflowRunTaskExec    string
	workflowRunSpecNode    string
	workflowRunCursor      string
	workflowRunAfterSeq    int64
	workflowRunFollowLogs  bool
	workflowRunLogsPollGap = 3 * time.Second
)

// workflowListCmd / workflowGetCmd / workflowReleaseCmd are the base workflow-noun
// verbs. They attach directly to `panda workflow` (there is no `workflow workflow`
// sub-noun), matching the `(base)` row in the CLI↔API table.
var workflowListCmd = &cobra.Command{
	Use:   "list",
	Short: "List workflows",
	Long: `List the workflows published from drafts (the executable objects).

Agents: 'workflow list' is for inspecting state or resolving a workflow the
user explicitly named — never for finding an existing workflow to run. A new
request starts with a fresh whiteboard → draft → review, not with reuse (see
'panda workflow docs').

Examples:
  panda workflow list
  panda workflow get <wf>
  panda workflow release <wf> <rel>`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		body, err := workflowGet(cmd.Context(), nil, "workflows")
		if err != nil {
			return err
		}

		return renderWorkflow(body, func(b []byte) {
			summarizeWorkflowItems(b, "items", "No workflows.")
		})
	},
}

var workflowGetCmd = &cobra.Command{
	Use:   "get <wf>",
	Short: "Get a workflow",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := workflowGet(cmd.Context(), nil, "workflows", args[0])
		if err != nil {
			return err
		}

		return renderWorkflow(body, summarizeWorkflowObject)
	},
}

var workflowReleaseCmd = &cobra.Command{
	Use:   "release <wf> <rel>",
	Short: "Get a workflow release",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := workflowGet(cmd.Context(), nil, "workflows", args[0], "releases", args[1])
		if err != nil {
			return err
		}

		return renderWorkflow(body, summarizeWorkflowObject)
	},
}

var workflowRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Create, list, inspect, follow, and cancel runs",
	Long: `Runs are single executions of a workflow. The status→exit contract
applies here: 'run watch' and 'run logs -f' set the exit code from the terminal
run.status (0 completed, non-zero failed/cancelled).

Examples:
  panda workflow run create <wf> --approved <wf> --json
  panda workflow run list <wf>
  panda workflow run watch <wf> <run> --json
  panda workflow run follow <wf> <run>      # background-friendly: deltas on stderr, summary on stdout
  panda workflow run logs <wf> <run> -f --json
  panda workflow run cancel <wf> <run>`,
	// Bare `panda workflow run <wf>` is a likely first guess: point at the two
	// real entry points instead of cobra's unknown-command error.
	RunE: func(cmd *cobra.Command, args []string) error {
		wf := "<wf>"
		if len(args) > 0 {
			wf = args[0]
		}

		cmd.Printf("to run an existing workflow:   panda workflow run create %s --approved %s\n", wf, wf)
		cmd.Println("to publish and run a draft:    panda workflow draft run <wb> <draft> --approved <draft>")

		return nil
	},
}

// requireRunApproval is the run-create tripwire: running an EXISTING workflow
// is both a side effect and a reuse decision, and neither is the agent's to
// make. The default path is a fresh whiteboard → draft → review → 'draft run';
// 'run create' is only for a workflow the user explicitly named, and still
// needs their explicit run approval, proven by re-typing the workflow id.
func requireRunApproval(approved, workflowID string) error {
	switch {
	case approved == "":
		return fmt.Errorf(`running an existing workflow crosses the side-effect boundary and requires --approved <workflowId>.

Reuse is not the default: unless the user explicitly named workflow %[1]s
(or said to re-run an existing workflow), start fresh instead — whiteboard →
draft → review ('draft show') → approval → 'draft run'. Do not go looking in
'workflow list' for something to run.

If the user DID name this workflow and explicitly approved running it:
  re-run this command with:  --approved %[1]s

Do not pass --approved on the user's behalf. See 'panda workflow docs'`, workflowID)
	case approved != workflowID:
		return fmt.Errorf(
			"--approved %s does not match the workflow argument %s — approval binds to one exact workflow",
			approved, workflowID)
	}

	return nil
}

var workflowRunCreateCmd = &cobra.Command{
	Use:   "create <wf>",
	Short: "Start a run of an existing workflow (side-effect boundary)",
	Long: `Start a run of an already-published workflow. This crosses the run
side-effect boundary AND reuses an existing workflow — only do it when the
user explicitly named this workflow (or asked to re-run one) and explicitly
approved running it; the default path for a new request is a fresh
whiteboard → draft → review → 'draft run' (see 'panda workflow docs').

Requires --approved <workflowId> (re-type the workflow id being run) as proof
of that approval.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireRunApproval(workflowRunApproved, args[0]); err != nil {
			return err
		}

		payload, err := buildRunBody(workflowRunInputs, workflowRunDispatch)
		if err != nil {
			return err
		}

		body, err := workflowSend(cmd.Context(), "POST", payload, nil, nil,
			"workflows", args[0], "runs")
		if err != nil {
			return err
		}

		return renderWorkflow(body, summarizeWorkflowObject)
	},
}

var workflowRunListCmd = &cobra.Command{
	Use:   "list <wf>",
	Short: "List a workflow's runs",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := workflowGet(cmd.Context(), nil, "workflows", args[0], "runs")
		if err != nil {
			return err
		}

		return renderWorkflow(body, func(b []byte) {
			summarizeWorkflowItems(b, "items", "No runs.")
		})
	},
}

var workflowRunGetCmd = &cobra.Command{
	Use:   "get <wf> <run>",
	Short: "Get a run's state (status, outputs, tasks)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := workflowGet(cmd.Context(), nil, "workflows", args[0], "runs", args[1], "state")
		if err != nil {
			return err
		}

		return renderWorkflow(body, summarizeRunState)
	},
}

var workflowRunWatchCmd = &cobra.Command{
	Use:   "watch <wf> <run>",
	Short: "Watch a run's state stream to terminal",
	Long: `Watch a run by streaming its state and refetching the /state snapshot
after each event (snapshot policy). Under --json each line is a full snapshot.
Exits at terminal run.status: 0 on completed, non-zero on failed/cancelled.
Ctrl-C exits cleanly.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return watchWorkflowState(
			commandContext(cmd),
			workflowRunAfterSeq,
			[]string{"workflows", args[0], "runs", args[1], "state"},
			[]string{"workflows", args[0], "runs", args[1], "state", "stream"},
			runSnapshotTerminal,
		)
	},
}

var workflowRunTasksCmd = &cobra.Command{
	Use:   "tasks <wf> <run>",
	Short: "List a run's tasks (spec-node keys for steering)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := workflowGet(cmd.Context(), nil, "workflows", args[0], "runs", args[1], "tasks")
		if err != nil {
			return err
		}

		return renderWorkflow(body, func(b []byte) {
			summarizeWorkflowItems(b, "items", "No tasks.")
		})
	},
}

var workflowRunLogsCmd = &cobra.Command{
	Use:   "logs <wf> <run>",
	Short: "Read or follow a run's worker log",
	Long: `Read a run's worker log. Without -f, a single GET of the log history.
With -f, follow the worker-log stream; because it carries no run.status, the
command polls .../runs/{run}/state out-of-band and exits at terminal run.status
(0 completed, non-zero failed/cancelled). --task-execution and --spec-node
filter; --cursor resumes.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		specNodes := nonEmptySlice(workflowRunSpecNode)
		taskExecs := nonEmptySlice(workflowRunTaskExec)

		if !workflowRunFollowLogs {
			body, err := workflowGet(cmd.Context(),
				workerLogQuery(workflowRunCursor, specNodes, taskExecs),
				"workflows", args[0], "runs", args[1], "worker-log")
			if err != nil {
				return err
			}

			return renderWorkflow(body, func(b []byte) {
				summarizeWorkerLog(b, "No log entries.")
			})
		}

		return followRunLogs(commandContext(cmd), args[0], args[1], workflowRunCursor, specNodes, taskExecs)
	},
}

var workflowRunCancelCmd = &cobra.Command{
	Use:   "cancel <wf> <run>",
	Short: "Cancel a run",
	Long: `Cancel a run. The engine returns 204 with no body, so nothing is parsed;
the run transitions to cancelled.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if _, err := workflowSend(cmd.Context(), "POST", nil, nil, nil,
			"workflows", args[0], "runs", args[1], "cancel"); err != nil {
			return err
		}

		if !isJSON() {
			cmd.Printf("run %s cancelled\n", args[1])
		}

		return nil
	},
}

func init() {
	// The base workflow-noun verbs (workflowListCmd/GetCmd/ReleaseCmd) are wired
	// onto workflowCmd in workflow.go's init.
	workflowRunCmd.AddCommand(
		workflowRunCreateCmd,
		workflowRunListCmd,
		workflowRunGetCmd,
		workflowRunWatchCmd,
		workflowRunFollowCmd,
		workflowRunTasksCmd,
		workflowRunLogsCmd,
		workflowRunCancelCmd,
	)

	workflowRunCreateCmd.Flags().StringVar(&workflowRunInputs, "inputs", "",
		"Run inputs {values,artifacts,secrets} as inline JSON or @file.json")
	workflowRunCreateCmd.Flags().StringVar(&workflowRunDispatch, "dispatch", "",
		"Dispatch policy overrides as inline JSON or @file.json")
	workflowRunCreateCmd.Flags().StringVar(&workflowRunApproved, "approved", "",
		"Workflow id the user explicitly named and approved running (must match the workflow argument)")
	workflowRunWatchCmd.Flags().Int64Var(&workflowRunAfterSeq, "after-seq", 0,
		"Resume the state stream after this numeric sequence")
	workflowRunLogsCmd.Flags().BoolVarP(&workflowRunFollowLogs, "follow", "f", false,
		"Follow the worker-log stream")
	workflowRunLogsCmd.Flags().StringVar(&workflowRunTaskExec, "task-execution", "",
		"Filter the worker log by task-execution id")
	workflowRunLogsCmd.Flags().StringVar(&workflowRunSpecNode, "spec-node", "",
		"Filter the worker log by spec-node key")
	workflowRunLogsCmd.Flags().StringVar(&workflowRunCursor, "cursor", "",
		"Resume the worker-log stream from this opaque cursor")

	for _, c := range []*cobra.Command{
		workflowRunCreateCmd,
		workflowRunListCmd,
		workflowRunGetCmd,
		workflowRunWatchCmd,
		workflowRunFollowCmd,
		workflowRunTasksCmd,
		workflowRunLogsCmd,
		workflowRunCancelCmd,
	} {
		c.ValidArgsFunction = noCompletions
	}
}

// summarizeRunState renders a run /state snapshot for text mode: the run id, its
// status, and the scalar run.outputs map (one key: value line each). The nested
// run fields (inputs, dispatchPolicy) and the surrounding envelope (tasks,
// operations, resources) are omitted, and non-scalar output values are skipped
// so a large nested payload never dumps. --json remains the full-body contract.
// A body without a `.run` object falls back to the generic object summary.
func summarizeRunState(body []byte) {
	var payload struct {
		Run *struct {
			ID      string         `json:"id"`
			Status  string         `json:"status"`
			Outputs map[string]any `json:"outputs"`
		} `json:"run"`
	}

	if err := json.Unmarshal(body, &payload); err != nil || payload.Run == nil {
		summarizeWorkflowObject(body)

		return
	}

	pairs := make([][2]string, 0, 2)

	if payload.Run.ID != "" {
		pairs = append(pairs, [2]string{"run", payload.Run.ID})
	}

	if payload.Run.Status != "" {
		pairs = append(pairs, [2]string{"status", payload.Run.Status})
	}

	if len(pairs) > 0 {
		printKeyValue(pairs)
	}

	printScalarMap("outputs", payload.Run.Outputs)
}

// printScalarMap prints a titled block of a map's scalar entries as indented
// `key: value` lines, skipping nested objects/arrays and nil values so a large
// nested payload never dumps. Nothing is printed when no scalar entries remain.
func printScalarMap(title string, m map[string]any) {
	if len(m) == 0 {
		return
	}

	keys := make([]string, 0, len(m))

	for k, v := range m {
		switch v.(type) {
		case nil, map[string]any, []any:
			// Skip nils and nested structures; --json shows them.
		default:
			keys = append(keys, k)
		}
	}

	if len(keys) == 0 {
		return
	}

	sort.Strings(keys)

	fmt.Printf("%s:\n", title)

	pairs := make([][2]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, [2]string{"  " + k, fmt.Sprint(m[k])})
	}

	printKeyValue(pairs)
}

// runSnapshotTerminal derives terminal exit from a run /state snapshot.
func runSnapshotTerminal(snapshot []byte) (bool, error) {
	status := runStatusFromState(snapshot)
	if status == "" || !isTerminalRunStatus(status) {
		return false, nil
	}

	return true, runStatusExitError(status)
}

// runStatusFromState reads `.run.status` from a run /state snapshot.
func runStatusFromState(snapshot []byte) string {
	var payload struct {
		Run struct {
			Status string `json:"status"`
		} `json:"run"`
	}

	if err := json.Unmarshal(snapshot, &payload); err != nil {
		return ""
	}

	return payload.Run.Status
}

// followRunLogs streams a run's worker log while polling .../state out-of-band
// for the terminal run.status the worker-log stream does not carry. The stream
// is opened on a cancellable context so that cancelling it (once the poll sees
// a terminal status) unblocks the SSE read promptly instead of waiting for the
// next heartbeat. It polls immediately, then every workflowRunLogsPollGap, so an
// already-completed run exits at once. It exits with the status-derived code;
// Ctrl-C exits cleanly.
func followRunLogs(ctx context.Context, wf, run, cursor string, specNodes, taskExecs []string) error {
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	resp, err := workflowStream(streamCtx, "GET", nil, sseHeaders(),
		workerLogQuery(cursor, specNodes, taskExecs),
		workflowPath("workflows", wf, "runs", run, "worker-log", "stream"))
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	var (
		mu             sync.Mutex
		terminalStatus string
		wg             sync.WaitGroup
	)

	wg.Go(func() {
		ticker := time.NewTicker(workflowRunLogsPollGap)
		defer ticker.Stop()

		for {
			status, pollErr := fetchRunStatus(streamCtx, wf, run)
			if pollErr == nil && isTerminalRunStatus(status) {
				mu.Lock()
				terminalStatus = status
				mu.Unlock()
				cancel()

				return
			}

			select {
			case <-streamCtx.Done():
				return
			case <-ticker.C:
			}
		}
	})

	streamErr := parseSSE(streamCtx, resp.Body, emitStreamItems)

	cancel()
	wg.Wait()

	mu.Lock()
	status := terminalStatus
	mu.Unlock()

	if status != "" {
		return runStatusExitError(status)
	}

	// The worker-log stream carries no run.status and is not guaranteed to close
	// at terminal, so it can EOF at completion before the poll (every
	// workflowRunLogsPollGap) observed a terminal status — or the poll may have been
	// erroring. Do one final synchronous /state read on the parent context (not
	// the now-cancelled streamCtx) so a failed/cancelled run still exits non-zero.
	// Skipped on user cancellation (Ctrl-C), where ctx.Err() is set.
	if ctx.Err() == nil {
		if finalStatus, ferr := fetchRunStatus(ctx, wf, run); ferr == nil && isTerminalRunStatus(finalStatus) {
			return runStatusExitError(finalStatus)
		}
	}

	return resolveStreamResult(ctx, streamErr, nil)
}

// fetchRunStatus reads the current run.status from the run /state endpoint.
func fetchRunStatus(ctx context.Context, wf, run string) (string, error) {
	body, err := workflowGet(ctx, nil, "workflows", wf, "runs", run, "state")
	if err != nil {
		return "", err
	}

	return runStatusFromState(body), nil
}

// nonEmptySlice wraps a non-empty value in a single-element slice, else nil.
func nonEmptySlice(value string) []string {
	if value == "" {
		return nil
	}

	return []string{value}
}
