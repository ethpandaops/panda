package cli

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"
)

var (
	workflowDispatchScope string
	workflowDispatchData  string
)

var workflowDispatchCmd = &cobra.Command{
	Use:   "dispatch",
	Short: "Inspect dispatch placement, agents, and workers",
	Long: `Inspect the dispatch inventory, effective policy, health, agents, and
worker identities/operations, and simulate placement decisions.

Examples:
  panda workflow dispatch inventory
  panda workflow dispatch effective
  panda workflow dispatch effective --scope org
  panda workflow dispatch simulate --data @sim.json
  panda workflow dispatch agents
  panda workflow dispatch workers
  panda workflow dispatch operations`,
}

var workflowDispatchInventoryCmd = &cobra.Command{
	Use:   "inventory",
	Short: "List (agent, model) pairs with healthy workers",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		body, err := workflowGet(cmd.Context(), nil, "dispatch", "inventory")
		if err != nil {
			return err
		}

		return renderWorkflow(body, func(b []byte) {
			summarizeWorkflowItems(b, "entries", "No inventory entries.")
		})
	},
}

var workflowDispatchEffectiveCmd = &cobra.Command{
	Use:   "effective",
	Short: "Show the effective dispatch policy",
	Long: `Show the effective dispatch policy. --scope me|org narrows it; when
--scope is unset the CLI sends no scope param and the engine applies its own default.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		body, err := workflowGet(cmd.Context(), dispatchEffectiveQuery(workflowDispatchScope),
			"dispatch", "effective")
		if err != nil {
			return err
		}

		return renderWorkflow(body, summarizeWorkflowObject)
	},
}

var workflowDispatchHealthCmd = &cobra.Command{
	Use:   "health",
	Short: "Show dispatch cooldowns/health",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		body, err := workflowGet(cmd.Context(), nil, "dispatch", "health")
		if err != nil {
			return err
		}

		return renderWorkflow(body, summarizeWorkflowObject)
	},
}

var workflowDispatchSimulateCmd = &cobra.Command{
	Use:   "simulate",
	Short: "Simulate a dispatch placement decision",
	Long: `Simulate a placement decision. --data is the request body (inline JSON
or @file.json), passed verbatim.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		data, err := readInlineOrFile(workflowDispatchData)
		if err != nil {
			return err
		}

		if len(data) == 0 {
			return fmt.Errorf("--data is required for 'dispatch simulate'")
		}

		body, err := workflowSend(cmd.Context(), "POST", data, nil, nil, "dispatch", "simulate")
		if err != nil {
			return err
		}

		return renderWorkflow(body, summarizeWorkflowObject)
	},
}

var workflowDispatchAgentsCmd = &cobra.Command{
	Use:   "agents",
	Short: "List agents and their workers",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		body, err := workflowGet(cmd.Context(), nil, "agents")
		if err != nil {
			return err
		}

		return renderWorkflow(body, summarizeWorkflowObject)
	},
}

var workflowDispatchWorkersCmd = &cobra.Command{
	Use:   "workers",
	Short: "List worker identities",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		body, err := workflowGet(cmd.Context(), nil, "worker-identities")
		if err != nil {
			return err
		}

		return renderWorkflow(body, func(b []byte) {
			summarizeWorkflowItems(b, "workerIdentities", "No worker identities.")
		})
	},
}

var workflowDispatchOperationsCmd = &cobra.Command{
	Use:   "operations",
	Short: "List queued/running worker operations",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		body, err := workflowGet(cmd.Context(), nil, "workers", "operations")
		if err != nil {
			return err
		}

		return renderWorkflow(body, func(b []byte) {
			summarizeWorkflowItems(b, "items", "No worker operations.")
		})
	},
}

func init() {
	workflowDispatchCmd.AddCommand(
		workflowDispatchInventoryCmd,
		workflowDispatchEffectiveCmd,
		workflowDispatchHealthCmd,
		workflowDispatchSimulateCmd,
		workflowDispatchAgentsCmd,
		workflowDispatchWorkersCmd,
		workflowDispatchOperationsCmd,
	)

	workflowDispatchEffectiveCmd.Flags().StringVar(&workflowDispatchScope, "scope", "",
		"Narrow the effective policy (me or org)")
	workflowDispatchSimulateCmd.Flags().StringVar(&workflowDispatchData, "data", "",
		"Simulation request body as inline JSON or @file.json")

	_ = workflowDispatchEffectiveCmd.RegisterFlagCompletionFunc("scope",
		cobra.FixedCompletions([]string{"me", "org"}, cobra.ShellCompDirectiveNoFileComp))
}

// dispatchEffectiveQuery builds the query for GET /dispatch/effective. It adds
// a scope param only when scope is non-empty; an unset scope sends no param so
// the engine applies its server default.
func dispatchEffectiveQuery(scope string) url.Values {
	if scope == "" {
		return nil
	}

	return url.Values{"scope": []string{scope}}
}
