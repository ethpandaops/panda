package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var doraCmd = &cobra.Command{
	GroupID: groupDirect,
	Use:     "dora",
	Short:   "Query Dora beacon chain explorer",
	Long: `Query the Dora beacon chain explorer for network status, validators, and slots.

Examples:
  panda dora networks
  panda dora overview hoodi
  panda dora validator hoodi 12345
  panda dora slot hoodi 1000000
  panda dora epoch hoodi 100`,
}

func init() {
	rootCmd.AddCommand(doraCmd)

	doraCmd.AddCommand(
		doraNetworksCmd,
		doraOverviewCmd,
		doraValidatorCmd,
		doraSlotCmd,
		doraEpochCmd,
	)

	doraOverviewCmd.ValidArgsFunction = completeNetworkNames
	doraValidatorCmd.ValidArgsFunction = completeNetworkNames
	doraSlotCmd.ValidArgsFunction = completeNetworkNames
	doraEpochCmd.ValidArgsFunction = completeNetworkNames
}

var doraNetworksCmd = &cobra.Command{
	Use:   "networks",
	Short: "List networks with Dora explorers",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		response, err := runServerOperation(cmd, "dora.list_networks", map[string]any{})
		if err != nil {
			return err
		}

		return printListing(response, "networks", "No networks with Dora explorers found.")
	},
}

var doraOverviewCmd = &cobra.Command{
	Use:   "overview <network>",
	Short: "Get network overview",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		response, err := runServerOperation(cmd, "dora.get_network_overview", map[string]any{
			"network": args[0],
		})
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSON(response)
		}

		data, _ := response.Data.(map[string]any)

		pairs := [][2]string{
			{"Network", args[0]},
			{"Current epoch", fmt.Sprintf("%v", data["current_epoch"])},
		}

		for _, kv := range [][2]string{
			{"Current slot", "current_slot"},
			{"Current epoch start slot", "current_epoch_start_slot"},
			{"Finalized epoch", "finalized_epoch"},
			{"Finalized epoch start slot", "finalized_epoch_start_slot"},
			{"Epochs since finality", "epochs_since_finality"},
			{"Finalizing", "finalizing"},
			{"Synced", "is_synced"},
		} {
			if value, ok := data[kv[1]]; ok {
				pairs = append(pairs, [2]string{kv[0], fmt.Sprintf("%v", value)})
			}
		}

		if participation, ok := data["participation"].(map[string]any); ok {
			if rate, ok := participation["rate"].(float64); ok {
				pairs = append(pairs, [2]string{"Participation rate", fmt.Sprintf("%.2f%%", rate)})
			}
		}

		for _, kv := range [][2]string{
			{"Active validators", "active_validator_count"},
			{"Total validators", "total_validator_count"},
			{"Pending validators", "pending_validator_count"},
			{"Exited validators", "exited_validator_count"},
		} {
			if value, ok := data[kv[1]]; ok {
				pairs = append(pairs, [2]string{kv[0], fmt.Sprintf("%v", value)})
			}
		}
		if warnings, ok := data["data_quality_warnings"]; ok {
			pairs = append(pairs, [2]string{"Data warning", formatDoraWarnings(warnings)})
		}

		printKeyValue(pairs)

		return nil
	},
}

// formatDoraWarnings joins a JSON-decoded warnings array into one line.
func formatDoraWarnings(value any) string {
	warnings, ok := value.([]any)
	if !ok {
		return fmt.Sprintf("%v", value)
	}

	parts := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		parts = append(parts, fmt.Sprintf("%v", warning))
	}

	return strings.Join(parts, "; ")
}

var doraValidatorCmd = &cobra.Command{
	Use:   "validator <network> <index-or-pubkey>",
	Short: "Get validator details (always JSON)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		response, err := runServerOperationRaw(cmd, "dora.get_validator", map[string]any{
			"network":         args[0],
			"index_or_pubkey": args[1],
		})
		if err != nil {
			return err
		}

		return printJSONBytes(response.Body)
	},
}

var doraSlotCmd = &cobra.Command{
	Use:   "slot <network> <slot-or-hash>",
	Short: "Get slot details (always JSON)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		response, err := runServerOperationRaw(cmd, "dora.get_slot", map[string]any{
			"network":      args[0],
			"slot_or_hash": args[1],
		})
		if err != nil {
			return err
		}

		return printJSONBytes(response.Body)
	},
}

var doraEpochCmd = &cobra.Command{
	Use:   "epoch <network> <epoch>",
	Short: "Get epoch summary (always JSON)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		response, err := runServerOperationRaw(cmd, "dora.get_epoch", map[string]any{
			"network": args[0],
			"epoch":   args[1],
		})
		if err != nil {
			return err
		}

		return printJSONBytes(response.Body)
	},
}
