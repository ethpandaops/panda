package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var ethnodeJSON bool

var ethnodeCmd = &cobra.Command{
	Use:   "ethnode",
	Short: "Query Ethereum beacon and execution nodes",
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
	ethnodeCmd.PersistentFlags().BoolVar(&ethnodeJSON, "json", false, "Output in JSON format")

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
		response, err := runServerOperation("ethnode.get_node_syncing", map[string]any{
			"network":  args[0],
			"instance": args[1],
		})
		if err != nil {
			return err
		}

		if ethnodeJSON {
			return printJSON(response.Data)
		}

		data := nestedMap(response.Data, "data")
		fmt.Printf("Head slot:     %v\n", data["head_slot"])
		fmt.Printf("Sync distance: %v\n", data["sync_distance"])
		fmt.Printf("Is syncing:    %v\n", data["is_syncing"])
		fmt.Printf("Is optimistic: %v\n", data["is_optimistic"])
		fmt.Printf("EL offline:    %v\n", data["el_offline"])

		return nil
	},
}

var ethNodeVersionCmd = &cobra.Command{
	Use:   "version <network> <instance>",
	Short: "Get node software version",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		beaconResp, err := runServerOperation("ethnode.get_node_version", map[string]any{
			"network":  args[0],
			"instance": args[1],
		})
		if err != nil {
			return err
		}

		if ethnodeJSON {
			executionResp, execErr := runServerOperation("ethnode.web3_client_version", map[string]any{
				"network":  args[0],
				"instance": args[1],
			})
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

		executionResp, execErr := runServerOperation("ethnode.web3_client_version", map[string]any{
			"network":  args[0],
			"instance": args[1],
		})
		if execErr != nil {
			_, _ = fmt.Fprintf(log.Writer(), "EL: (error: %v)\n", execErr)
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
	RunE: func(_ *cobra.Command, args []string) error {
		response, err := runServerOperation("ethnode.get_node_health", map[string]any{
			"network":  args[0],
			"instance": args[1],
		})
		if err != nil {
			return err
		}

		data, _ := response.Data.(map[string]any)
		statusValue := intFromAny(data["status_code"])

		if ethnodeJSON {
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
	RunE: func(_ *cobra.Command, args []string) error {
		response, err := runServerOperation("ethnode.get_peer_count", map[string]any{
			"network":  args[0],
			"instance": args[1],
		})
		if err != nil {
			return err
		}

		if ethnodeJSON {
			return printJSON(response.Data)
		}

		data := nestedMap(response.Data, "data")
		fmt.Printf("Connected:     %v\n", data["connected"])
		fmt.Printf("Disconnected:  %v\n", data["disconnected"])
		fmt.Printf("Connecting:    %v\n", data["connecting"])
		fmt.Printf("Disconnecting: %v\n", data["disconnecting"])

		return nil
	},
}

var ethNodeFinalityCmd = &cobra.Command{
	Use:   "finality <network> <instance>",
	Short: "Get finality checkpoints",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		response, err := runServerOperation("ethnode.get_finality_checkpoints", map[string]any{
			"network":  args[0],
			"instance": args[1],
		})
		if err != nil {
			return err
		}

		if ethnodeJSON {
			return printJSON(response.Data)
		}

		data := nestedMap(response.Data, "data")
		fmt.Printf("Finalized:          epoch %v\n", nestedMap(data["finalized"], "")["epoch"])
		fmt.Printf("Current justified:  epoch %v\n", nestedMap(data["current_justified"], "")["epoch"])
		fmt.Printf("Previous justified: epoch %v\n", nestedMap(data["previous_justified"], "")["epoch"])

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

		response, err := runServerOperation("ethnode.get_beacon_headers", map[string]any{
			"network":  args[0],
			"instance": args[1],
			"slot":     slot,
		})
		if err != nil {
			return err
		}

		if ethnodeJSON {
			return printJSON(response.Data)
		}

		data := nestedMap(response.Data, "data")
		header := nestedMap(data["header"], "")
		message := nestedMap(header["message"], "")

		fmt.Printf("Slot:           %v\n", message["slot"])
		fmt.Printf("Proposer index: %v\n", message["proposer_index"])
		fmt.Printf("Root:           %v\n", data["root"])
		fmt.Printf("Parent root:    %v\n", message["parent_root"])
		fmt.Printf("State root:     %v\n", message["state_root"])
		fmt.Printf("Body root:      %v\n", message["body_root"])

		return nil
	},
}

var ethNodeBlockNumberCmd = &cobra.Command{
	Use:   "block-number <network> <instance>",
	Short: "Get latest execution layer block number",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		response, err := runServerOperation("ethnode.eth_block_number", map[string]any{
			"network":  args[0],
			"instance": args[1],
		})
		if err != nil {
			return err
		}

		data, _ := response.Data.(map[string]any)
		if ethnodeJSON {
			return printJSON(data)
		}

		fmt.Println(intFromAny(data["block_number"]))
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

		response, err := runServerOperationRaw("ethnode.beacon_get", map[string]any{
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

		response, err := runServerOperationRaw("ethnode.execution_rpc", map[string]any{
			"network":  args[0],
			"instance": args[1],
			"method":   args[2],
			"params":   params,
		})
		if err != nil {
			return err
		}

		if ethnodeJSON {
			return printJSONBytes(response.Body)
		}

		var payload map[string]any
		if err := json.Unmarshal(response.Body, &payload); err != nil {
			return printJSONBytes(response.Body)
		}

		return printJSON(payload["result"])
	},
}

func nestedMap(value any, key string) map[string]any {
	if key == "" {
		data, _ := value.(map[string]any)
		return data
	}

	data, _ := value.(map[string]any)
	nested, _ := data[key].(map[string]any)
	return nested
}

func intFromAny(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case int:
		return int64(typed)
	case int64:
		return typed
	default:
		return 0
	}
}
