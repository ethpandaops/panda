package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var doraJSON bool

var doraCmd = &cobra.Command{
	Use:   "dora",
	Short: "Query Dora beacon chain explorer",
	Long: `Query the Dora beacon chain explorer for network status, validators, and slots.

Examples:
  ep dora networks
  ep dora overview hoodi
  ep dora validator hoodi 12345
  ep dora slot hoodi 1000000
  ep dora epoch hoodi 100`,
}

func init() {
	rootCmd.AddCommand(doraCmd)
	doraCmd.PersistentFlags().BoolVar(&doraJSON, "json", false, "Output in JSON format")

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

		if doraJSON {
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

		if doraJSON {
			return printJSON(response)
		}

		data, _ := response.Data.(map[string]any)
		fmt.Printf("Network:            %s\n", args[0])
		fmt.Printf("Current epoch:      %v\n", data["current_epoch"])
		fmt.Printf("Current slot:       %v\n", data["current_slot"])
		fmt.Printf("Finalized:          %v\n", data["finalized"])
		fmt.Printf("Participation rate: %v\n", data["participation_rate"])

		if value, ok := data["active_validator_count"]; ok {
			fmt.Printf("Active validators:  %v\n", value)
		}
		if value, ok := data["total_validator_count"]; ok {
			fmt.Printf("Total validators:   %v\n", value)
		}
		if value, ok := data["pending_validator_count"]; ok {
			fmt.Printf("Pending validators: %v\n", value)
		}
		if value, ok := data["exited_validator_count"]; ok {
			fmt.Printf("Exited validators:  %v\n", value)
		}

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
