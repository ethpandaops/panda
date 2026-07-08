package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"github.com/spf13/cobra"
)

var (
	workflowWhiteboardName         string
	workflowWhiteboardRequirements string
	workflowWhiteboardInputs       string
	workflowWatchAfterSeq          int64
)

var workflowWhiteboardCmd = &cobra.Command{
	Use:     "whiteboard",
	Aliases: []string{"wb"},
	Short:   "List, create, inspect, and watch whiteboards",
	Long: `Whiteboards are the workflow-engine planning space that holds sessions and drafts.

Agents: a new request gets a NEW whiteboard ('whiteboard create').
'whiteboard list' is for inspecting state or resolving a whiteboard the user
explicitly named — never for finding an existing one to continue from (see
'panda workflow docs').

Examples:
  panda workflow whiteboard list
  panda workflow whiteboard create --requirements "count blocks per day"
  panda workflow whiteboard get <wb>
  panda workflow whiteboard watch <wb> --json`,
}

var workflowWhiteboardListCmd = &cobra.Command{
	Use:   "list",
	Short: "List whiteboards",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		body, err := workflowGet(cmd.Context(), nil, "whiteboards")
		if err != nil {
			return err
		}

		return renderWorkflow(body, func(b []byte) {
			summarizeWorkflowItems(b, "items", "No whiteboards found.")
		})
	},
}

var workflowWhiteboardCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a whiteboard",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		inputs, err := readJSONFlag("--inputs", workflowWhiteboardInputs)
		if err != nil {
			return err
		}

		payload, err := buildWhiteboardCreateBody(workflowWhiteboardName, workflowWhiteboardRequirements, inputs)
		if err != nil {
			return err
		}

		body, err := workflowSend(cmd.Context(), "POST", payload, nil, nil, "whiteboards")
		if err != nil {
			return err
		}

		return renderWorkflow(body, summarizeWorkflowObject)
	},
}

var workflowWhiteboardGetCmd = &cobra.Command{
	Use:   "get <wb>",
	Short: "Get a whiteboard state snapshot",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := workflowGet(cmd.Context(), nil, "whiteboards", args[0], "state")
		if err != nil {
			return err
		}

		return renderWorkflow(body, summarizeWhiteboardState)
	},
}

var workflowWhiteboardWatchCmd = &cobra.Command{
	Use:   "watch <wb>",
	Short: "Watch a whiteboard's state stream (snapshot per event)",
	Long: `Watch a whiteboard by streaming its state and refetching the /state
snapshot after each event. Under --json each line is a full snapshot (NDJSON of
snapshots), never the raw delta. Exits non-zero only on a stream/transport
error; Ctrl-C exits cleanly.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return watchWorkflowState(
			commandContext(cmd),
			workflowWatchAfterSeq,
			[]string{"whiteboards", args[0], "state"},
			[]string{"whiteboards", args[0], "state", "stream"},
			nil,
		)
	},
}

func init() {
	workflowWhiteboardCmd.AddCommand(
		workflowWhiteboardListCmd,
		workflowWhiteboardCreateCmd,
		workflowWhiteboardGetCmd,
		workflowWhiteboardWatchCmd,
	)

	workflowWhiteboardCreateCmd.Flags().StringVar(&workflowWhiteboardName, "name", "",
		"Whiteboard name")
	workflowWhiteboardCreateCmd.Flags().StringVar(&workflowWhiteboardRequirements, "requirements", "",
		"Plain-language requirements for the whiteboard")
	workflowWhiteboardCreateCmd.Flags().StringVar(&workflowWhiteboardInputs, "inputs", "",
		"Inputs object as inline JSON or @file.json")
	workflowWhiteboardWatchCmd.Flags().Int64Var(&workflowWatchAfterSeq, "after-seq", 0,
		"Resume the state stream after this numeric sequence")

	workflowWhiteboardGetCmd.ValidArgsFunction = completeWorkflowWhiteboardIDs
	workflowWhiteboardWatchCmd.ValidArgsFunction = completeWorkflowWhiteboardIDs
}

// buildWhiteboardCreateBody assembles the POST /whiteboards body. inputs, when
// present, is embedded verbatim at `.inputs`.
func buildWhiteboardCreateBody(name, requirements string, inputs []byte) ([]byte, error) {
	body := struct {
		Name         string          `json:"name,omitempty"`
		Requirements string          `json:"requirements,omitempty"`
		Inputs       json.RawMessage `json:"inputs,omitempty"`
	}{
		Name:         name,
		Requirements: requirements,
		Inputs:       json.RawMessage(inputs),
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("building whiteboard body: %w", err)
	}

	return data, nil
}

// summarizeWhiteboardState renders a whiteboard /state snapshot for text mode:
// the whiteboard id/name/status, its latestDraftId/latestSessionId pointers, and
// a count of drafts/sessions. The nested whiteboard object's remaining fields and
// the full drafts/sessions arrays are omitted; --json remains the stable, full-
// body contract. A body without a `.whiteboard` object falls back to the generic
// object summary.
func summarizeWhiteboardState(body []byte) {
	var payload struct {
		Whiteboard *struct {
			ID              string `json:"id"`
			Name            string `json:"name"`
			Status          string `json:"status"`
			LatestDraftID   string `json:"latestDraftId"`
			LatestSessionID string `json:"latestSessionId"`
		} `json:"whiteboard"`
		Drafts   []json.RawMessage `json:"drafts"`
		Sessions []json.RawMessage `json:"sessions"`
	}

	if err := json.Unmarshal(body, &payload); err != nil || payload.Whiteboard == nil {
		summarizeWorkflowObject(body)

		return
	}

	wb := payload.Whiteboard
	pairs := make([][2]string, 0, 7)

	for _, kv := range [][2]string{
		{"whiteboard", wb.ID},
		{"name", wb.Name},
		{"status", wb.Status},
		{"latestDraftId", wb.LatestDraftID},
		{"latestSessionId", wb.LatestSessionID},
	} {
		if kv[1] != "" {
			pairs = append(pairs, kv)
		}
	}

	pairs = append(pairs,
		[2]string{"drafts", strconv.Itoa(len(payload.Drafts))},
		[2]string{"sessions", strconv.Itoa(len(payload.Sessions))},
	)

	printKeyValue(pairs)
}

// watchWorkflowState implements the snapshot policy: fetch the initial /state
// snapshot, emit it, then open the state stream and refetch + emit the snapshot
// after each event. terminalFn (nil for whiteboards) derives an exit code from
// a snapshot; a nil terminalFn never terminates on status. Exits non-zero only
// on a stream/transport error; Ctrl-C exits cleanly.
func watchWorkflowState(
	ctx context.Context,
	afterSeq int64,
	statePath, streamPath []string,
	terminalFn func([]byte) (bool, error),
) error {
	snapshot, err := workflowGet(ctx, nil, statePath...)
	if err != nil {
		return err
	}

	if err := emitSnapshot(snapshot); err != nil {
		return err
	}

	if terminalFn != nil {
		if done, exitErr := terminalFn(snapshot); done {
			return exitErr
		}
	}

	query := url.Values{}
	if afterSeq > 0 {
		query.Set("afterSeq", strconv.FormatInt(afterSeq, 10))
	}

	resp, err := workflowStream(ctx, "GET", nil, sseHeaders(), query, workflowPath(streamPath...))
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	var termErr error

	streamErr := parseSSE(ctx, resp.Body, func(_ sseEvent) error {
		snap, refetchErr := workflowGet(ctx, nil, statePath...)
		if refetchErr != nil {
			return refetchErr
		}

		if emitErr := emitSnapshot(snap); emitErr != nil {
			return emitErr
		}

		if terminalFn != nil {
			if done, exitErr := terminalFn(snap); done {
				termErr = exitErr

				return errStreamComplete
			}
		}

		return nil
	})

	return resolveStreamResult(ctx, streamErr, termErr)
}
