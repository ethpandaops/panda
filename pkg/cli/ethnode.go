package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/ethpandaops/panda/pkg/operations"
	"github.com/spf13/cobra"
)

var ethnodeCmd = &cobra.Command{
	GroupID: groupDirect,
	Use:     "ethnode",
	Short:   "Query Ethereum beacon and execution nodes",
	Long: `Direct access to Ethereum beacon and execution node APIs.
Nodes are identified by network and instance name (e.g., "lighthouse-geth-1").

Examples:
  panda ethnode syncing dencun-devnet-12 lighthouse-geth-1
  panda ethnode peers dencun-devnet-12 lighthouse-geth-1
  panda ethnode finality dencun-devnet-12 lighthouse-geth-1
  panda ethnode beacon-get dencun-devnet-12 lighthouse-geth-1 /eth/v1/node/identity`,
}

func init() {
	rootCmd.AddCommand(ethnodeCmd)

	ethnodeCmd.AddCommand(
		ethNodeSyncingCmd,
		ethNodeVersionCmd,
		ethNodeHealthCmd,
		ethNodePeersCmd,
		ethNodeFinalityCmd,
		ethNodeHeaderCmd,
		ethNodeBlockNumberCmd,
		ethNodeBeaconGetCmd,
		ethNodeExecRPCCmd,
	)

	ethNodeSyncingCmd.ValidArgsFunction = completeNetworkNames
	ethNodeVersionCmd.ValidArgsFunction = completeNetworkNames
	ethNodeHealthCmd.ValidArgsFunction = completeNetworkNames
	ethNodePeersCmd.ValidArgsFunction = completeNetworkNames
	ethNodeFinalityCmd.ValidArgsFunction = completeNetworkNames
	ethNodeHeaderCmd.ValidArgsFunction = completeNetworkNames
	ethNodeBlockNumberCmd.ValidArgsFunction = completeNetworkNames
	ethNodeBeaconGetCmd.ValidArgsFunction = completeNetworkNames
	ethNodeExecRPCCmd.ValidArgsFunction = completeNetworkNames
}

var ethNodeSyncingCmd = &cobra.Command{
	Use:   "syncing <network> <instance>",
	Short: "Get beacon node sync status",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		response, err := ethNodeSyncing(operations.EthNodeNodeArgs{
			Network:  args[0],
			Instance: args[1],
		})
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSON(response)
		}

		fmt.Printf("Head slot:     %s\n", response.Data.HeadSlot)
		fmt.Printf("Sync distance: %s\n", response.Data.SyncDistance)
		fmt.Printf("Is syncing:    %t\n", response.Data.IsSyncing)
		fmt.Printf("Is optimistic: %t\n", response.Data.IsOptimistic)
		fmt.Printf("EL offline:    %t\n", response.Data.ELOffline)

		return nil
	},
}

var ethNodeVersionCmd = &cobra.Command{
	Use:   "version <network> <instance>",
	Short: "Get node software version",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		request := operations.EthNodeNodeArgs{
			Network:  args[0],
			Instance: args[1],
		}

		beaconResp, err := ethNodeVersion(request)
		if err != nil {
			return err
		}

		if isJSON() {
			executionResp, execErr := ethNodeExecutionClientVersion(request)
			if execErr != nil {
				return printJSON(map[string]any{
					"beacon":          beaconResp,
					"execution_error": execErr.Error(),
				})
			}

			return printJSON(map[string]any{
				"beacon":    beaconResp,
				"execution": executionResp,
			})
		}

		fmt.Printf("CL: %s\n", beaconResp.Data.Version)

		executionResp, execErr := ethNodeExecutionClientVersion(request)
		if execErr != nil {
			_, _ = fmt.Fprintf(os.Stderr, "EL: (error: %v)\n", execErr)
			return nil
		}

		fmt.Printf("EL: %s\n", executionResp)
		return nil
	},
}

var ethNodeHealthCmd = &cobra.Command{
	Use:   "health <network> <instance>",
	Short: "Get beacon node health status",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		response, err := ethNodeHealth(operations.EthNodeNodeArgs{
			Network:  args[0],
			Instance: args[1],
		})
		if err != nil {
			return err
		}

		statusValue := response.StatusCode

		if isJSON() {
			return printJSON(response)
		}

		switch statusValue {
		case 200:
			fmt.Println("Healthy (200)")
		case 206:
			fmt.Println("Syncing (206)")
		case 503:
			fmt.Println("Not initialized (503)")
		default:
			fmt.Printf("Status: %d\n", statusValue)
		}

		return nil
	},
}

var ethNodePeersCmd = &cobra.Command{
	Use:   "peers <network> <instance>",
	Short: "Get peer count",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		response, err := ethNodePeerCount(operations.EthNodeNodeArgs{
			Network:  args[0],
			Instance: args[1],
		})
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSON(response)
		}

		fmt.Printf("Connected:     %s\n", response.Data.Connected)
		fmt.Printf("Disconnected:  %s\n", response.Data.Disconnected)
		fmt.Printf("Connecting:    %s\n", response.Data.Connecting)
		fmt.Printf("Disconnecting: %s\n", response.Data.Disconnecting)

		return nil
	},
}

var ethNodeFinalityCmd = &cobra.Command{
	Use:   "finality <network> <instance>",
	Short: "Get finality checkpoints",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		response, err := ethNodeFinality(operations.EthNodeFinalityArgs{
			Network:  args[0],
			Instance: args[1],
		})
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSON(response)
		}

		fmt.Printf("Finalized:          epoch %s\n", response.Data.Finalized.Epoch)
		fmt.Printf("Current justified:  epoch %s\n", response.Data.CurrentJustified.Epoch)
		fmt.Printf("Previous justified: epoch %s\n", response.Data.PreviousJustified.Epoch)

		return nil
	},
}

var ethNodeHeaderCmd = &cobra.Command{
	Use:   "header <network> <instance> [slot]",
	Short: "Get beacon block header",
	Args:  cobra.RangeArgs(2, 3),
	RunE: func(_ *cobra.Command, args []string) error {
		slot := "head"
		if len(args) == 3 {
			slot = args[2]
		}

		response, err := ethNodeHeaders(operations.EthNodeBeaconHeadersArgs{
			Network:  args[0],
			Instance: args[1],
			Slot:     slot,
		})
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSON(response)
		}

		fmt.Printf("Slot:           %s\n", response.Data.Header.Message.Slot)
		fmt.Printf("Proposer index: %s\n", response.Data.Header.Message.ProposerIndex)
		fmt.Printf("Root:           %s\n", response.Data.Root)
		fmt.Printf("Parent root:    %s\n", response.Data.Header.Message.ParentRoot)
		fmt.Printf("State root:     %s\n", response.Data.Header.Message.StateRoot)
		fmt.Printf("Body root:      %s\n", response.Data.Header.Message.BodyRoot)

		return nil
	},
}

var ethNodeBlockNumberCmd = &cobra.Command{
	Use:   "block-number <network> <instance>",
	Short: "Get latest execution layer block number",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		response, err := ethNodeBlockNumber(operations.EthNodeNodeArgs{
			Network:  args[0],
			Instance: args[1],
		})
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSON(response)
		}

		fmt.Println(response.BlockNumber)
		return nil
	},
}

var ethNodeBeaconGetCmd = &cobra.Command{
	Use:   "beacon-get <network> <instance> <path>",
	Short: "GET any beacon API endpoint",
	Long: `Make a GET request to any beacon API endpoint.
The path should start with / (e.g., /eth/v1/node/identity).

Examples:
  panda ethnode beacon-get my-devnet lighthouse-geth-1 /eth/v1/node/identity
  panda ethnode beacon-get my-devnet lighthouse-geth-1 /eth/v1/config/deposit_contract`,
	Args: cobra.ExactArgs(3),
	RunE: func(_ *cobra.Command, args []string) error {
		path := args[2]
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}

		response, err := ethNodeBeaconGet(operations.EthNodeBeaconGetArgs{
			Network:  args[0],
			Instance: args[1],
			Path:     path,
		})
		if err != nil {
			return err
		}

		return printJSONBytes(response.Body)
	},
}

var ethNodeExecRPCCmd = &cobra.Command{
	Use:   "exec-rpc <network> <instance> <method> [params-json]",
	Short: "Call any JSON-RPC method on an execution node",
	Long: `Make a JSON-RPC call to any execution node method.
Params should be a JSON array string.

Examples:
  panda ethnode exec-rpc my-devnet lighthouse-geth-1 eth_blockNumber
  panda ethnode exec-rpc my-devnet lighthouse-geth-1 eth_getBlockByNumber '["latest", false]'
  panda ethnode exec-rpc my-devnet lighthouse-geth-1 eth_chainId`,
	Args: cobra.RangeArgs(3, 4),
	RunE: func(_ *cobra.Command, args []string) error {
		var params []any
		if len(args) == 4 {
			if err := json.Unmarshal([]byte(args[3]), &params); err != nil {
				return fmt.Errorf("invalid params JSON (must be an array): %w", err)
			}
		}

		response, err := ethNodeExecutionRPC(operations.EthNodeExecutionRPCArgs{
			Network:  args[0],
			Instance: args[1],
			Method:   args[2],
			Params:   params,
		})
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSONBytes(response.Body)
		}

		var payload map[string]any
		if err := json.Unmarshal(response.Body, &payload); err != nil {
			return printJSONBytes(response.Body)
		}

		if str, ok := payload["result"].(string); ok {
			fmt.Println(str)
			return nil
		}

		return printJSON(payload["result"])
	},
}
