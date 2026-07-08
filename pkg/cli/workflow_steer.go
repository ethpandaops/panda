package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

var (
	workflowSteerMessage string
	workflowSteerDismiss bool
	workflowSteerRetry   bool
)

var workflowSteerCmd = &cobra.Command{
	Use:   "steer",
	Short: "Redirect a running task mid-run",
	Long: `Steer a running task without cancelling it: the task interrupts,
applies your direction, and still finishes with validated output. <task> is the
task's specNodeKey from 'run tasks' (e.g. tasks.analyze); for a loop, steer the
inner iteration node (tasks.<loop>.<child>[iter=NNNN]), not the parent.

Examples:
  panda workflow steer send <wf> <run> <task> --message "only report Japan"
  panda workflow steer queue <wf> <run> <task>
  panda workflow steer turns <wf> <run> <task>`,
}

var workflowSteerSendCmd = &cobra.Command{
	Use:   "send <wf> <run> <task>",
	Short: "Send a steer message to a running task",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		if workflowSteerMessage == "" {
			return fmt.Errorf("--message is required for 'steer send'")
		}

		payload, err := buildSteerBody(workflowSteerMessage)
		if err != nil {
			return err
		}

		body, err := workflowSend(cmd.Context(), "POST", payload, nil, nil,
			"workflows", args[0], "runs", args[1], "task-executions", args[2], "steer")
		if err != nil {
			return err
		}

		return renderWorkflow(body, summarizeWorkflowObject)
	},
}

var workflowSteerQueueCmd = &cobra.Command{
	Use:   "queue <wf> <run> <task>",
	Short: "Show a task's steer queue",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := workflowGet(cmd.Context(), nil,
			"workflows", args[0], "runs", args[1], "task-executions", args[2], "queue")
		if err != nil {
			return err
		}

		return renderWorkflow(body, summarizeWorkflowObject)
	},
}

var workflowSteerTurnsCmd = &cobra.Command{
	Use:   "turns <wf> <run> <task>",
	Short: "List a task's steer turns",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := workflowGet(cmd.Context(), nil,
			"workflows", args[0], "runs", args[1], "task-executions", args[2], "turns")
		if err != nil {
			return err
		}

		return renderWorkflow(body, func(b []byte) {
			summarizeWorkflowItems(b, "items", "No turns.")
		})
	},
}

var workflowSteerQueueItemCmd = &cobra.Command{
	Use:   "queue-item <wf> <run> <task> <itemId>",
	Short: "Dismiss or retry a steer queue item",
	Long: `Act on a steer queue item by id. Exactly one of --dismiss (retract the
item) or --retry (only valid on a parked item) must be given. Both <task> and
<itemId> are percent-encoded so reserved characters (e.g. [ ] =) survive.`,
	Args: cobra.ExactArgs(4),
	RunE: func(cmd *cobra.Command, args []string) error {
		action, err := steerQueueItemAction(workflowSteerDismiss, workflowSteerRetry)
		if err != nil {
			return err
		}

		body, err := workflowSend(cmd.Context(), "POST", nil, nil, nil,
			"workflows", args[0], "runs", args[1], "task-executions", args[2],
			"queue", args[3], action)
		if err != nil {
			return err
		}

		return renderWorkflow(body, summarizeWorkflowObject)
	},
}

func init() {
	workflowSteerCmd.AddCommand(
		workflowSteerSendCmd,
		workflowSteerQueueCmd,
		workflowSteerTurnsCmd,
		workflowSteerQueueItemCmd,
	)

	workflowSteerSendCmd.Flags().StringVar(&workflowSteerMessage, "message", "",
		"Steer direction to apply to the running task (required)")
	workflowSteerQueueItemCmd.Flags().BoolVar(&workflowSteerDismiss, "dismiss", false,
		"Dismiss (retract) the queue item")
	workflowSteerQueueItemCmd.Flags().BoolVar(&workflowSteerRetry, "retry", false,
		"Retry a parked queue item")

	for _, c := range []*cobra.Command{
		workflowSteerSendCmd,
		workflowSteerQueueCmd,
		workflowSteerTurnsCmd,
		workflowSteerQueueItemCmd,
	} {
		c.ValidArgsFunction = noCompletions
	}
}

// buildSteerBody assembles the steer body {message}.
func buildSteerBody(message string) ([]byte, error) {
	data, err := json.Marshal(map[string]any{"message": message})
	if err != nil {
		return nil, fmt.Errorf("building steer body: %w", err)
	}

	return data, nil
}

// steerQueueItemAction maps the dismiss/retry flags to a path suffix. Exactly
// one flag must be set.
func steerQueueItemAction(dismiss, retry bool) (string, error) {
	switch {
	case dismiss && retry:
		return "", fmt.Errorf("only one of --dismiss or --retry may be given")
	case dismiss:
		return "dismiss", nil
	case retry:
		return "retry", nil
	default:
		return "", fmt.Errorf("exactly one of --dismiss or --retry is required")
	}
}
