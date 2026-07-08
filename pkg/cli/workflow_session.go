package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

var (
	workflowSessionTitle     string
	workflowSessionContent   string
	workflowSessionInterrupt bool
	workflowSessionIdemKey   string
	workflowSessionFollow    bool
	workflowSessionCursor    string
	workflowQueueItemRetry   bool
	workflowQueueItemSkip    bool
	workflowQueueItemDelete  bool
)

var workflowSessionCmd = &cobra.Command{
	Use:     "session",
	Aliases: []string{"ses"},
	Short:   "Drive whiteboard chat sessions with the drafting worker",
	Long: `Sessions are your chat with the workflow-engine worker that writes drafts. Send
plain-language requests and let the engine draft; do not hand-author specs.

Examples:
  panda workflow session create <wb> --content "count blocks per day"
  panda workflow session send <wb> <sid> --content "make it a loop"
  panda workflow session logs <wb> <sid> -f --json
  panda workflow session turns <wb> <sid>`,
}

var workflowSessionCreateCmd = &cobra.Command{
	Use:   "create <wb>",
	Short: "Create a session (optionally with a first message)",
	Long: `Create a session on a whiteboard. --content is optional: with it the
CLI sends an initialItem (mode:queue); without it no initialItem is sent and the
first message goes via 'session send'.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		payload, err := buildSessionCreateBody(workflowSessionTitle, workflowSessionContent)
		if err != nil {
			return err
		}

		body, err := workflowSend(cmd.Context(), "POST", payload, nil, nil,
			"whiteboards", args[0], "sessions")
		if err != nil {
			return err
		}

		return renderWorkflow(body, summarizeWorkflowObject)
	},
}

var workflowSessionSendCmd = &cobra.Command{
	Use:   "send <wb> <sid>",
	Short: "Send a message to a session",
	Long: `Send a message to a session. Defaults to mode:queue (starts a turn when
idle, enqueues behind a live turn). --interrupt selects mode:stop_and_send. A
random UUIDv4 Idempotency-Key is minted per invocation; override it with
--idempotency-key for cross-retry dedup.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if workflowSessionContent == "" {
			return fmt.Errorf("--content is required for 'session send'")
		}

		payload, err := buildSessionSendBody(workflowSessionContent, workflowSessionInterrupt)
		if err != nil {
			return err
		}

		key := workflowSessionIdemKey
		if key == "" {
			key = uuid.NewString()
		}

		headers := map[string]string{"Idempotency-Key": key}

		body, err := workflowSend(cmd.Context(), "POST", payload, headers, nil,
			"whiteboards", args[0], "sessions", args[1], "items")
		if err != nil {
			return err
		}

		return renderWorkflow(body, summarizeWorkflowObject)
	},
}

var workflowSessionLogsCmd = &cobra.Command{
	Use:   "logs <wb> <sid>",
	Short: "Read or follow a session's worker log",
	Long: `Read a session's worker log. Without -f, a single GET of the log
history. With -f, follow the worker-log stream; it exits 0 on the FIRST
turn/operation-terminal event (a queued item starts a NEW turn that is not
followed — re-invoke to follow it), and non-zero only on a stream/transport
error. --cursor resumes an opaque worker-log cursor.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if !workflowSessionFollow {
			body, err := workflowGet(cmd.Context(), workerLogQuery(workflowSessionCursor, nil, nil),
				"whiteboards", args[0], "sessions", args[1], "worker-log")
			if err != nil {
				return err
			}

			return renderWorkflow(body, func(b []byte) {
				summarizeWorkerLog(b, "No log entries.")
			})
		}

		return followSessionLogs(commandContext(cmd), args[0], args[1], workflowSessionCursor)
	},
}

var workflowSessionTurnsCmd = &cobra.Command{
	Use:   "turns <wb> <sid>",
	Short: "List a session's turns",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := workflowGet(cmd.Context(), nil, "whiteboards", args[0], "sessions", args[1], "turns")
		if err != nil {
			return err
		}

		return renderWorkflow(body, func(b []byte) {
			summarizeWorkflowItems(b, "items", "No turns.")
		})
	},
}

var workflowSessionQueueCmd = &cobra.Command{
	Use:   "queue <wb> <sid>",
	Short: "Show a session's queue (parked + pending)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := workflowGet(cmd.Context(), nil, "whiteboards", args[0], "sessions", args[1], "queue")
		if err != nil {
			return err
		}

		return renderWorkflow(body, summarizeWorkflowObject)
	},
}

var workflowSessionResumeCmd = &cobra.Command{
	Use:   "resume <wb> <sid>",
	Short: "Resume a session",
	Args:  cobra.ExactArgs(2),
	RunE:  runSessionAction("resume"),
}

var workflowSessionSkipCmd = &cobra.Command{
	Use:   "skip <wb> <sid>",
	Short: "Skip the session's pending elicitation",
	Args:  cobra.ExactArgs(2),
	RunE:  runSessionAction("skip"),
}

var workflowSessionInterruptCmd = &cobra.Command{
	Use:   "interrupt <wb> <sid>",
	Short: "Interrupt the session's active worker operation",
	Args:  cobra.ExactArgs(2),
	RunE:  runSessionAction("interrupt"),
}

var workflowSessionQueueItemCmd = &cobra.Command{
	Use:   "queue-item <wb> <sid> <itemId>",
	Short: "Retry, skip, or delete a session queue item",
	Long: `Act on a session queue item by id. Exactly one of --retry (only valid
on a parked item), --skip, or --delete must be given.`,
	Args: cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		method, action, err := queueItemAction(
			workflowQueueItemRetry, workflowQueueItemSkip, workflowQueueItemDelete)
		if err != nil {
			return err
		}

		segments := []string{"whiteboards", args[0], "sessions", args[1], "queue", args[2]}
		if action != "" {
			segments = append(segments, action)
		}

		body, err := workflowSend(cmd.Context(), method, nil, nil, nil, segments...)
		if err != nil {
			return err
		}

		return renderWorkflow(body, summarizeWorkflowObject)
	},
}

func init() {
	workflowSessionCmd.AddCommand(
		workflowSessionCreateCmd,
		workflowSessionSendCmd,
		workflowSessionLogsCmd,
		workflowSessionTurnsCmd,
		workflowSessionQueueCmd,
		workflowSessionResumeCmd,
		workflowSessionSkipCmd,
		workflowSessionInterruptCmd,
		workflowSessionQueueItemCmd,
	)

	workflowSessionCreateCmd.Flags().StringVar(&workflowSessionTitle, "title", "", "Session title")
	workflowSessionCreateCmd.Flags().StringVar(&workflowSessionContent, "content", "",
		"First message content (optional)")
	workflowSessionSendCmd.Flags().StringVar(&workflowSessionContent, "content", "", "Message content")
	workflowSessionSendCmd.Flags().BoolVar(&workflowSessionInterrupt, "interrupt", false,
		"Interrupt the live turn and send now (mode:stop_and_send)")
	workflowSessionSendCmd.Flags().StringVar(&workflowSessionIdemKey, "idempotency-key", "",
		"Idempotency-Key override (default: a fresh UUIDv4 per invocation)")
	workflowSessionLogsCmd.Flags().BoolVarP(&workflowSessionFollow, "follow", "f", false,
		"Follow the worker-log stream")
	workflowSessionLogsCmd.Flags().StringVar(&workflowSessionCursor, "cursor", "",
		"Resume the worker-log stream from this opaque cursor")

	workflowSessionQueueItemCmd.Flags().BoolVar(&workflowQueueItemRetry, "retry", false,
		"Retry a parked queue item")
	workflowSessionQueueItemCmd.Flags().BoolVar(&workflowQueueItemSkip, "skip", false,
		"Skip the queue item")
	workflowSessionQueueItemCmd.Flags().BoolVar(&workflowQueueItemDelete, "delete", false,
		"Delete (retract) the queue item")

	for _, c := range []*cobra.Command{
		workflowSessionCreateCmd,
		workflowSessionSendCmd,
		workflowSessionLogsCmd,
		workflowSessionTurnsCmd,
		workflowSessionQueueCmd,
		workflowSessionResumeCmd,
		workflowSessionSkipCmd,
		workflowSessionInterruptCmd,
		workflowSessionQueueItemCmd,
	} {
		c.ValidArgsFunction = completeWorkflowWhiteboardIDs
	}
}

// runSessionAction returns a RunE that POSTs a session control action.
func runSessionAction(action string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		body, err := workflowSend(cmd.Context(), "POST", nil, nil, nil,
			"whiteboards", args[0], "sessions", args[1], action)
		if err != nil {
			return err
		}

		return renderWorkflow(body, summarizeWorkflowObject)
	}
}

// buildSessionCreateBody assembles the POST .../sessions body. When content is
// empty, NO initialItem is sent (a partial initialItem is rejected 500); when
// present it is minted with mode:queue.
func buildSessionCreateBody(title, content string) ([]byte, error) {
	body := map[string]any{}

	if title != "" {
		body["title"] = title
	}

	if content != "" {
		body["initialItem"] = map[string]any{
			"type":    "message",
			"mode":    "queue",
			"content": content,
		}
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("building session body: %w", err)
	}

	return data, nil
}

// buildSessionSendBody assembles the POST .../items body. mode defaults to
// "queue"; --interrupt selects "stop_and_send".
func buildSessionSendBody(content string, interrupt bool) ([]byte, error) {
	mode := "queue"
	if interrupt {
		mode = "stop_and_send"
	}

	data, err := json.Marshal(map[string]any{
		"type":    "message",
		"mode":    mode,
		"content": content,
	})
	if err != nil {
		return nil, fmt.Errorf("building message body: %w", err)
	}

	return data, nil
}

// queueItemAction maps the retry/skip/delete flags to an HTTP method and path
// suffix. Exactly one flag must be set. --retry → POST .../retry; --skip →
// POST .../skip; --delete → DELETE (no suffix).
func queueItemAction(retry, skip, del bool) (method, action string, err error) {
	count := 0
	for _, b := range []bool{retry, skip, del} {
		if b {
			count++
		}
	}

	if count != 1 {
		return "", "", fmt.Errorf("exactly one of --retry, --skip, or --delete is required")
	}

	switch {
	case retry:
		return "POST", "retry", nil
	case skip:
		return "POST", "skip", nil
	default:
		return "DELETE", "", nil
	}
}

// workerLogQuery builds the query for a worker-log read/stream: cursor plus
// optional spec-node and task-execution filters (repeatable array params).
func workerLogQuery(cursor string, specNodes, taskExecutions []string) url.Values {
	query := url.Values{}

	if cursor != "" {
		query.Set("cursor", cursor)
	}

	for _, node := range specNodes {
		query.Add("specNodeIds[]", node)
	}

	for _, task := range taskExecutions {
		query.Add("taskExecutionIds[]", task)
	}

	if len(query) == 0 {
		return nil
	}

	return query
}

// followSessionLogs streams a session's worker log, flattening each `page`
// frame into per-item output and exiting 0 on the first item whose `.type` is a
// turn/operation terminal. The worker-log stream frames every event as
// `event: page`, so terminal detection reads the item types inside the data
// payload, NOT the SSE `event:` frame name.
func followSessionLogs(ctx context.Context, wb, sid, cursor string) error {
	resp, err := workflowStream(ctx, "GET", nil, sseHeaders(),
		workerLogQuery(cursor, nil, nil),
		workflowPath("whiteboards", wb, "sessions", sid, "worker-log", "stream"))
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	streamErr := parseSSE(ctx, resp.Body, func(ev sseEvent) error {
		if emitErr := emitStreamItems(ev); emitErr != nil {
			return emitErr
		}

		if workerLogTerminalType([]byte(ev.Data)) {
			return errStreamComplete
		}

		return nil
	})

	return resolveStreamResult(ctx, streamErr, nil)
}

// sessionTerminalEvents is the set of worker-log event names that end a
// session's turn/operation.
var sessionTerminalEvents = map[string]struct{}{
	"worker.operation.completed":   {},
	"worker.operation.failed":      {},
	"worker.operation.cancelled":   {},
	"worker.operation.interrupted": {},
	"turn.completed":               {},
	"turn.failed":                  {},
	"turn.interrupted":             {},
	"whiteboard.turn.completed":    {},
	"whiteboard.session.failed":    {},
}

// isSessionTerminalEvent reports whether a session worker-log item `.type` ends
// the current turn or operation.
func isSessionTerminalEvent(event string) bool {
	_, ok := sessionTerminalEvents[event]

	return ok
}

// workerLogTerminalType reports whether a worker-log `page` frame's data
// payload contains any item whose `.type` ends the session's current turn or
// operation. A payload that is not a page (empty, `sync`, or a non-batch shape)
// reports false.
func workerLogTerminalType(data []byte) bool {
	if len(data) == 0 {
		return false
	}

	var page workerLogPage
	if err := json.Unmarshal(data, &page); err != nil {
		return false
	}

	for _, raw := range page.Items {
		var item struct {
			Type string `json:"type"`
		}

		if err := json.Unmarshal(raw, &item); err != nil {
			continue
		}

		if isSessionTerminalEvent(item.Type) {
			return true
		}
	}

	return false
}
