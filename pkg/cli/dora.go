package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var doraCmd = &cobra.Command{
	Use:   "dora",
	Short: "Query Dora beacon chain explorer",
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
	RunE: func(_ *cobra.Command, _ []string) error {
		response, err := runServerOperation("dora.list_networks", map[string]any{})
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSON(response)
		}

		data, _ := response.Data.(map[string]any)
		items, _ := data["networks"].([]any)
		if len(items) == 0 {
			fmt.Println("No networks with Dora explorers found.")
			return nil
		}

		for _, item := range items {
			network, _ := item.(map[string]any)
			name, _ := network["name"].(string)
			doraURL, _ := network["dora_url"].(string)
			fmt.Printf("  %-30s  %s\n", name, doraURL)
		}

		return nil
	},
}

var doraOverviewCmd = &cobra.Command{
	Use:   "overview <network>",
	Short: "Get network overview",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		response, err := runServerOperation("dora.get_network_overview", map[string]any{
			"network": args[0],
		})
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSON(response)
		}

		data, _ := response.Data.(map[string]any)

		// Format participation rate as a percentage.
		participationStr := fmt.Sprintf("%v", data["participation_rate"])
		if rate, ok := data["participation_rate"].(float64); ok {
			participationStr = fmt.Sprintf("%.2f%%", rate*100)
		}

		pairs := [][2]string{
			{"Network", args[0]},
			{"Current epoch", fmt.Sprintf("%v", data["current_epoch"])},
			{"Epoch finalized", fmt.Sprintf("%v", data["finalized"])},
			{"Participation rate", participationStr},
		}

		if value, ok := data["active_validator_count"]; ok {
			pairs = append(pairs, [2]string{"Active validators", fmt.Sprintf("%v", value)})
		}
		if value, ok := data["total_validator_count"]; ok {
			pairs = append(pairs, [2]string{"Total validators", fmt.Sprintf("%v", value)})
		}
		if value, ok := data["pending_validator_count"]; ok {
			pairs = append(pairs, [2]string{"Pending validators", fmt.Sprintf("%v", value)})
		}
		if value, ok := data["exited_validator_count"]; ok {
			pairs = append(pairs, [2]string{"Exited validators", fmt.Sprintf("%v", value)})
		}

		printKeyValue(pairs)

		return nil
	},
}

var doraValidatorCmd = &cobra.Command{
	Use:   "validator <network> <index-or-pubkey>",
	Short: "Get validator details",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		response, err := runServerOperationRaw("dora.get_validator", map[string]any{
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
	Short: "Get slot details",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		response, err := runServerOperationRaw("dora.get_slot", map[string]any{
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
	Short: "Get epoch summary",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		response, err := runServerOperationRaw("dora.get_epoch", map[string]any{
			"network": args[0],
			"epoch":   args[1],
		})
		if err != nil {
			return err
		}

		return printJSONBytes(response.Body)
	},
}
