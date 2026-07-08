package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

var (
	workflowDraftSpec     string
	workflowDraftInputs   string
	workflowDraftDispatch string
	workflowDraftApproved string
)

// requireDraftApproval is the tripwire in front of the publish/run side-effect
// boundary: the caller must re-type the exact draft id as --approved, proving
// the review checkpoint happened against THIS draft (clig.dev's
// "--confirm=<name-of-thing>" pattern — hard to pass by accident, still
// scriptable). A stale approval (older draft id after a new revision) fails the
// match by construction. The flag is proof of review, not the approval itself —
// that must come from the user.
func requireDraftApproval(approved, draftID string) error {
	switch {
	case approved == "":
		return fmt.Errorf(`this command crosses the publish/run side-effect boundary and requires --approved <draftId>.

The flag is proof the review checkpoint happened — not a formality to skip:
  1. render the draft for the user:  panda workflow draft show <wb> %[1]s
  2. get their explicit publish/run approval for THIS draft
  3. re-run this command with:       --approved %[1]s

If the user has not explicitly approved, stop and ask them — do not pass
--approved on their behalf. Approval rules: 'panda workflow docs'`, draftID)
	case approved != draftID:
		return fmt.Errorf(
			"--approved %s does not match the draft argument %s — approval binds to one exact draft; "+
				"if a newer revision appeared, re-review it (draft show) and get approval for its id",
			approved, draftID)
	}

	return nil
}

var workflowDraftCmd = &cobra.Command{
	Use:   "draft",
	Short: "List, inspect, revise, publish, and run drafts",
	Long: `Drafts are candidate workflow specs on a whiteboard. The engine owns
drafting — iterate via 'session send' rather than hand-authoring specs.

Examples:
  panda workflow draft list <wb>
  panda workflow draft show <wb> <draft>
  panda workflow draft get <wb> <draft> --json
  panda workflow draft run <wb> <draft> --approved <draft> --json`,
}

var workflowDraftListCmd = &cobra.Command{
	Use:   "list <wb>",
	Short: "List a whiteboard's drafts",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := workflowGet(cmd.Context(), nil, "whiteboards", args[0], "drafts")
		if err != nil {
			return err
		}

		return renderWorkflow(body, func(b []byte) {
			summarizeWorkflowItems(b, "items", "No drafts.")
		})
	},
}

var workflowDraftGetCmd = &cobra.Command{
	Use:   "get <wb> <draft>",
	Short: "Get a draft (graph, inputs, spec)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := workflowGet(cmd.Context(), nil, "whiteboards", args[0], "drafts", args[1])
		if err != nil {
			return err
		}

		return renderWorkflow(body, summarizeWorkflowObject)
	},
}

var workflowDraftReviseCmd = &cobra.Command{
	Use:   "revise <wb> <draft>",
	Short: "Post a hand-authored spec revision (expert escape hatch)",
	Long: `Post a hand-authored authoredSpecYaml to the engine's manual-revision
endpoint. This is an expert/manual escape hatch — the normal path is to iterate
via 'session send' and let the engine draft. --spec is required (inline YAML or
@file); --inputs is an optional provided-inputs override.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		spec, err := readInlineOrFile(workflowDraftSpec)
		if err != nil {
			return err
		}

		if len(spec) == 0 {
			return fmt.Errorf("--spec is required (inline YAML or @file.yaml)")
		}

		inputs, err := readJSONFlag("--inputs", workflowDraftInputs)
		if err != nil {
			return err
		}

		payload, err := buildDraftRevisionBody(spec, inputs)
		if err != nil {
			return err
		}

		body, err := workflowSend(cmd.Context(), "POST", payload, nil, nil,
			"whiteboards", args[0], "drafts", args[1], "revisions")
		if err != nil {
			return err
		}

		return renderWorkflow(body, summarizeWorkflowObject)
	},
}

var workflowDraftPublishCmd = &cobra.Command{
	Use:   "publish <wb> <draft>",
	Short: "Publish a draft into a workflow (side-effect boundary)",
	Long: `Publish a draft into an executable workflow. This crosses the
publish/run side-effect boundary — get the user's explicit approval for this
exact draft first; the original task request alone is not approval (see
'panda workflow docs').

Requires --approved <draftId> (re-type the draft id being published) as proof
the user reviewed and approved this exact draft.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireDraftApproval(workflowDraftApproved, args[1]); err != nil {
			return err
		}

		body, err := workflowSend(cmd.Context(), "POST", nil, nil, nil,
			"whiteboards", args[0], "drafts", args[1], "publish")
		if err != nil {
			return err
		}

		return renderWorkflow(body, summarizeWorkflowObject)
	},
}

var workflowDraftRunCmd = &cobra.Command{
	Use:   "run <wb> <draft>",
	Short: "Publish and run a draft in one step (side-effect boundary)",
	Long: `Publish a draft and start a run in one call. This crosses the
publish/run side-effect boundary in a single step — do not invoke it until the
user has seen this exact draft (use 'draft show') and explicitly approved
publishing/running it; the original task request alone is not approval (see
'panda workflow docs').

Requires --approved <draftId> (re-type the draft id being run) as proof the
user reviewed and approved this exact draft. A stale id from a superseded
revision is rejected.

Inputs are usually optional (the engine defaults them); override with --inputs, and
pin placement with --dispatch. Both accept inline JSON or @file.json and are
placed at body.inputs and body.dispatchPolicy.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireDraftApproval(workflowDraftApproved, args[1]); err != nil {
			return err
		}

		payload, err := buildRunBody(workflowDraftInputs, workflowDraftDispatch)
		if err != nil {
			return err
		}

		body, err := workflowSend(cmd.Context(), "POST", payload, nil, nil,
			"whiteboards", args[0], "drafts", args[1], "run")
		if err != nil {
			return err
		}

		return renderWorkflow(body, summarizeWorkflowObject)
	},
}

func init() {
	workflowDraftCmd.AddCommand(
		workflowDraftListCmd,
		workflowDraftGetCmd,
		workflowDraftShowCmd,
		workflowDraftReviseCmd,
		workflowDraftPublishCmd,
		workflowDraftRunCmd,
	)

	workflowDraftReviseCmd.Flags().StringVar(&workflowDraftSpec, "spec", "",
		"authoredSpecYaml as inline YAML or @file.yaml (required)")
	workflowDraftReviseCmd.Flags().StringVar(&workflowDraftInputs, "inputs", "",
		"Provided-inputs override as inline JSON or @file.json")
	workflowDraftRunCmd.Flags().StringVar(&workflowDraftInputs, "inputs", "",
		"Run inputs {values,artifacts,secrets} as inline JSON or @file.json")
	workflowDraftRunCmd.Flags().StringVar(&workflowDraftDispatch, "dispatch", "",
		"Dispatch policy overrides as inline JSON or @file.json")

	for _, c := range []*cobra.Command{workflowDraftPublishCmd, workflowDraftRunCmd} {
		c.Flags().StringVar(&workflowDraftApproved, "approved", "",
			"Draft id the user explicitly approved (must match the draft argument)")
	}

	for _, c := range []*cobra.Command{
		workflowDraftListCmd,
		workflowDraftGetCmd,
		workflowDraftShowCmd,
		workflowDraftReviseCmd,
		workflowDraftPublishCmd,
		workflowDraftRunCmd,
	} {
		c.ValidArgsFunction = completeWorkflowWhiteboardIDs
	}
}

// buildDraftRevisionBody assembles the manual-revision body. spec is the
// authoredSpecYaml (a YAML string carried as a JSON string); inputs, when
// present, is embedded verbatim at `.inputs`.
func buildDraftRevisionBody(spec, inputs []byte) ([]byte, error) {
	body := struct {
		AuthoredSpecYaml string          `json:"authoredSpecYaml"`
		Inputs           json.RawMessage `json:"inputs,omitempty"`
	}{
		AuthoredSpecYaml: string(spec),
		Inputs:           json.RawMessage(inputs),
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("building revision body: %w", err)
	}

	return data, nil
}

// buildRunBody assembles a run body {inputs, dispatchPolicy} from the inline-
// or-@file flag values, embedding each verbatim. An empty result marshals to
// `{}`, which workflow treats as default inputs/placement.
func buildRunBody(inputsFlag, dispatchFlag string) ([]byte, error) {
	inputs, err := readJSONFlag("--inputs", inputsFlag)
	if err != nil {
		return nil, err
	}

	dispatch, err := readJSONFlag("--dispatch", dispatchFlag)
	if err != nil {
		return nil, err
	}

	body := struct {
		Inputs         json.RawMessage `json:"inputs,omitempty"`
		DispatchPolicy json.RawMessage `json:"dispatchPolicy,omitempty"`
	}{
		Inputs:         json.RawMessage(inputs),
		DispatchPolicy: json.RawMessage(dispatch),
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("building run body: %w", err)
	}

	return data, nil
}
