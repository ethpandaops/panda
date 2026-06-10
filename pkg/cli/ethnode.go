package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var ethnodeCmd = &cobra.Command{
	GroupID: groupDirect,
	Use:     "ethnode",
	Short:   "Query Ethereum beacon and execution nodes",
	Long: `Direct access to Ethereum beacon and execution node APIs.
Nodes are identified by network id and instance name. Use 'panda ethnode
networks' to discover network ids before calling a node endpoint. Instance
names come from the network's node inventory or observability data; direct node
access does not enumerate every instance.

Examples:
  panda ethnode list-datasources
  panda ethnode networks
  panda ethnode syncing <network-id> <instance>
  panda ethnode peers <network-id> <instance>
  panda ethnode finality <network-id> <instance>
  panda ethnode beacon-get <network-id> <instance> /eth/v1/node/identity`,
}

func init() {
	rootCmd.AddCommand(ethnodeCmd)

	ethnodeCmd.AddCommand(
		ethNodeListDatasourcesCmd,
		ethNodeNetworksCmd,
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

	ethNodeSyncingCmd.ValidArgsFunction = completeEthNodeArgs
	ethNodeVersionCmd.ValidArgsFunction = completeEthNodeArgs
	ethNodeHealthCmd.ValidArgsFunction = completeEthNodeArgs
	ethNodePeersCmd.ValidArgsFunction = completeEthNodeArgs
	ethNodeFinalityCmd.ValidArgsFunction = completeEthNodeArgs
	ethNodeHeaderCmd.ValidArgsFunction = completeEthNodeArgs
	ethNodeBlockNumberCmd.ValidArgsFunction = completeEthNodeArgs
	ethNodeBeaconGetCmd.ValidArgsFunction = completeEthNodeArgs
	ethNodeExecRPCCmd.ValidArgsFunction = completeEthNodeArgs
}

var ethNodeListDatasourcesCmd = &cobra.Command{
	Use:   "list-datasources",
	Short: "List available ethnode datasources",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		response, err := runServerOperation(cmd, "ethnode.list_datasources", map[string]any{})
		if err != nil {
			return err
		}

		return printDatasourceList(response)
	},
}

var ethNodeNetworksCmd = &cobra.Command{
	Use:   "networks",
	Short: "List active networks reachable for direct node access",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		response, err := runServerOperation(cmd, "ethnode.list_networks", map[string]any{})
		if err != nil {
			return err
		}

		return printListing(response, "networks", "No ethnode-reachable active networks found.")
	},
}

var ethNodeSyncingCmd = &cobra.Command{
	Use:   "syncing <network> <instance>",
	Short: "Get beacon node sync status",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		response, err := runServerOperation(cmd, "ethnode.get_node_syncing", map[string]any{
			"network":  args[0],
			"instance": args[1],
		})
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSON(response.Data)
		}

		data := nestedMap(response.Data, "data")
		printKeyValue([][2]string{
			{"Head slot", fmt.Sprintf("%v", data["head_slot"])},
			{"Sync distance", fmt.Sprintf("%v", data["sync_distance"])},
			{"Is syncing", fmt.Sprintf("%v", data["is_syncing"])},
			{"Is optimistic", fmt.Sprintf("%v", data["is_optimistic"])},
			{"EL offline", fmt.Sprintf("%v", data["el_offline"])},
		})

		return nil
	},
}

var ethNodeVersionCmd = &cobra.Command{
	Use:   "version <network> <instance>",
	Short: "Get node software version",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		beaconResp, err := runServerOperation(cmd, "ethnode.get_node_version", map[string]any{
			"network":  args[0],
			"instance": args[1],
		})
		if err != nil {
			return err
		}

		executionResp, execErr := runServerOperation(cmd, "ethnode.web3_client_version", map[string]any{
			"network":  args[0],
			"instance": args[1],
		})

		if isJSON() {
			if execErr != nil {
				return printJSON(map[string]any{
					"beacon":          beaconResp.Data,
					"execution_error": execErr.Error(),
				})
			}

			return printJSON(map[string]any{
				"beacon":    beaconResp.Data,
				"execution": executionResp.Data,
			})
		}

		fmt.Printf("CL: %v\n", nestedMap(beaconResp.Data, "data")["version"])

		if execErr != nil {
			_, _ = fmt.Fprintf(os.Stderr, "EL: (error: %v)\n", execErr)
			return nil
		}

		fmt.Printf("EL: %v\n", executionResp.Data)
		return nil
	},
}

var ethNodeHealthCmd = &cobra.Command{
	Use:   "health <network> <instance>",
	Short: "Get beacon node health status",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		response, err := runServerOperation(cmd, "ethnode.get_node_health", map[string]any{
			"network":  args[0],
			"instance": args[1],
		})
		if err != nil {
			return err
		}

		data, _ := response.Data.(map[string]any)
		statusValue := intFromAny(data["status_code"])

		if isJSON() {
			return printJSON(response.Data)
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
	RunE: func(cmd *cobra.Command, args []string) error {
		response, err := runServerOperation(cmd, "ethnode.get_peer_count", map[string]any{
			"network":  args[0],
			"instance": args[1],
		})
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSON(response.Data)
		}

		data := nestedMap(response.Data, "data")
		connected := intFromAny(data["connected"])
		disconnected := intFromAny(data["disconnected"])
		connecting := intFromAny(data["connecting"])
		disconnecting := intFromAny(data["disconnecting"])
		total := connected + disconnected + connecting + disconnecting

		printKeyValue([][2]string{
			{"Connected", fmt.Sprintf("%d", connected)},
			{"Disconnected", fmt.Sprintf("%d", disconnected)},
			{"Connecting", fmt.Sprintf("%d", connecting)},
			{"Disconnecting", fmt.Sprintf("%d", disconnecting)},
			{"Total", fmt.Sprintf("%d", total)},
		})

		return nil
	},
}

var ethNodeFinalityCmd = &cobra.Command{
	Use:   "finality <network> <instance>",
	Short: "Get finality checkpoints",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		response, err := runServerOperation(cmd, "ethnode.get_finality_checkpoints", map[string]any{
			"network":  args[0],
			"instance": args[1],
		})
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSON(response.Data)
		}

		data := nestedMap(response.Data, "data")
		finalized := nestedMap(data["finalized"], "")
		justified := nestedMap(data["current_justified"], "")
		prevJustified := nestedMap(data["previous_justified"], "")

		printKeyValue([][2]string{
			{"Finalized", fmt.Sprintf("epoch %v (root: %v)", finalized["epoch"], finalized["root"])},
			{"Current justified", fmt.Sprintf("epoch %v (root: %v)", justified["epoch"], justified["root"])},
			{"Previous justified", fmt.Sprintf("epoch %v (root: %v)", prevJustified["epoch"], prevJustified["root"])},
		})

		return nil
	},
}

var ethNodeHeaderCmd = &cobra.Command{
	Use:   "header <network> <instance> [slot]",
	Short: "Get beacon block header",
	Args:  cobra.RangeArgs(2, 3),
	RunE: func(cmd *cobra.Command, args []string) error {
		slot := "head"
		if len(args) == 3 {
			slot = args[2]
		}

		response, err := runServerOperation(cmd, "ethnode.get_beacon_headers", map[string]any{
			"network":  args[0],
			"instance": args[1],
			"slot":     slot,
		})
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSON(response.Data)
		}

		data := nestedMap(response.Data, "data")
		header := nestedMap(data["header"], "")
		message := nestedMap(header["message"], "")

		printKeyValue([][2]string{
			{"Slot", fmt.Sprintf("%v", message["slot"])},
			{"Proposer index", fmt.Sprintf("%v", message["proposer_index"])},
			{"Root", fmt.Sprintf("%v", data["root"])},
			{"Parent root", fmt.Sprintf("%v", message["parent_root"])},
		})

		return nil
	},
}

var ethNodeBlockNumberCmd = &cobra.Command{
	Use:   "block-number <network> <instance>",
	Short: "Get latest execution layer block number",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		response, err := runServerOperation(cmd, "ethnode.eth_block_number", map[string]any{
			"network":  args[0],
			"instance": args[1],
		})
		if err != nil {
			return err
		}

		data, _ := response.Data.(map[string]any)
		if isJSON() {
			return printJSON(data)
		}

		fmt.Println(intFromAny(data["block_number"]))
		return nil
	},
}

var ethNodeBeaconGetCmd = &cobra.Command{
	Use:   "beacon-get <network> <instance> <path>",
	Short: "GET any beacon API endpoint (always JSON)",
	Long: `Make a GET request to any beacon API endpoint.
The path should start with / (e.g., /eth/v1/node/identity).

Examples:
  panda ethnode beacon-get <network-id> <instance> /eth/v1/node/identity
  panda ethnode beacon-get <network-id> <instance> /eth/v1/config/deposit_contract`,
	Args: cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		path := args[2]
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}

		response, err := runServerOperationRaw(cmd, "ethnode.beacon_get", map[string]any{
			"network":  args[0],
			"instance": args[1],
			"path":     path,
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
  panda ethnode exec-rpc <network-id> <instance> eth_blockNumber
  panda ethnode exec-rpc <network-id> <instance> eth_getBlockByNumber '["latest", false]'
  panda ethnode exec-rpc <network-id> <instance> eth_chainId`,
	Args: cobra.RangeArgs(3, 4),
	RunE: func(cmd *cobra.Command, args []string) error {
		var params []any
		if len(args) == 4 {
			if err := json.Unmarshal([]byte(args[3]), &params); err != nil {
				return fmt.Errorf("invalid params JSON (must be an array): %w", err)
			}
		}

		response, err := runServerOperationRaw(cmd, "ethnode.execution_rpc", map[string]any{
			"network":  args[0],
			"instance": args[1],
			"method":   args[2],
			"params":   params,
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
