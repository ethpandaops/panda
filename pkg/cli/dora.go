package cli

import (
	"fmt"

	"github.com/ethpandaops/panda/pkg/operations"
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
	RunE: func(_ *cobra.Command, _ []string) error {
		networks, err := listDoraNetworks()
		if err != nil {
			return err
		}

		if doraJSON || isJSON() {
			return printJSON(operations.DoraNetworksPayload{Networks: networks})
		}

		if len(networks) == 0 {
			fmt.Println("No networks with Dora explorers found.")
			return nil
		}

		for _, network := range networks {
			fmt.Printf("  %-30s  %s\n", network.Name, network.DoraURL)
		}

		return nil
	},
}

var doraOverviewCmd = &cobra.Command{
	Use:   "overview <network>",
	Short: "Get network overview",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		response, err := doraOverview(operations.DoraNetworkArgs{Network: args[0]})
		if err != nil {
			return err
		}

		if doraJSON || isJSON() {
			return printJSON(response)
		}

		participationStr := fmt.Sprintf("%v", response.ParticipationRate)
		if rate, ok := response.ParticipationRate.(float64); ok {
			participationStr = fmt.Sprintf("%.2f%%", rate*100)
		}

		pairs := [][2]string{
			{"Network", args[0]},
			{"Current epoch", fmt.Sprintf("%v", response.CurrentEpoch)},
			{"Current slot", fmt.Sprintf("%v", response.CurrentSlot)},
			{"Epoch finalized", fmt.Sprintf("%v", response.Finalized)},
			{"Participation rate", participationStr},
		}

		if value := response.ActiveValidatorCount; value != nil {
			pairs = append(pairs, [2]string{"Active validators", fmt.Sprintf("%v", value)})
		}
		if value := response.TotalValidatorCount; value != nil {
			pairs = append(pairs, [2]string{"Total validators", fmt.Sprintf("%v", value)})
		}
		if value := response.PendingValidatorCount; value != nil {
			pairs = append(pairs, [2]string{"Pending validators", fmt.Sprintf("%v", value)})
		}
		if value := response.ExitedValidatorCount; value != nil {
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
		response, err := doraValidator(operations.DoraIndexOrPubkeyArgs{
			Network:       args[0],
			IndexOrPubkey: args[1],
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
		response, err := doraSlot(operations.DoraSlotOrHashArgs{
			Network:    args[0],
			SlotOrHash: args[1],
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
		response, err := doraEpoch(operations.DoraEpochArgs{
			Network: args[0],
			Epoch:   args[1],
		})
		if err != nil {
			return err
		}

		return printJSONBytes(response.Body)
	},
}
