package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ethpandaops/mcp/pkg/proxy"
)

var ethnodeJSON bool

var ethnodeCmd = &cobra.Command{
	Use:   "ethnode",
	Short: "Query Ethereum beacon and execution nodes",
	Long: `Direct access to Ethereum beacon and execution node APIs.
Nodes are identified by network and instance name (e.g., "lighthouse-geth-1").

Examples:
  ep ethnode syncing dencun-devnet-12 lighthouse-geth-1
  ep ethnode peers dencun-devnet-12 lighthouse-geth-1
  ep ethnode finality dencun-devnet-12 lighthouse-geth-1
  ep ethnode beacon-get dencun-devnet-12 lighthouse-geth-1 /eth/v1/node/identity`,
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
}

// beaconGet makes a GET request to a beacon node via the proxy.
func beaconGet(ctx context.Context, pc proxy.Client, network, instance, path string) ([]byte, error) {
	return proxyGet(ctx, pc, fmt.Sprintf("/beacon/%s/%s%s", network, instance, path), nil, nil)
}

// execRPC makes a JSON-RPC call to an execution node via the proxy.
func execRPC(ctx context.Context, pc proxy.Client, network, instance, method string, params []any) (json.RawMessage, error) {
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
		"id":      1,
	})
	if err != nil {
		return nil, err
	}

	data, err := proxyPost(ctx, pc, fmt.Sprintf("/execution/%s/%s/", network, instance),
		strings.NewReader(string(payload)), nil,
		map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return nil, err
	}

	var rpcResp struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal(data, &rpcResp); err != nil {
		return nil, fmt.Errorf("parsing JSON-RPC response: %w", err)
	}

	if rpcResp.Error != nil {
		return nil, fmt.Errorf("JSON-RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	return rpcResp.Result, nil
}

// hexToUint64 parses a hex string like "0x1a2b" to uint64.
func hexToUint64(s string) (uint64, error) {
	s = strings.TrimPrefix(s, "0x")

	var val uint64

	for _, c := range s {
		val <<= 4

		switch {
		case c >= '0' && c <= '9':
			val += uint64(c - '0')
		case c >= 'a' && c <= 'f':
			val += uint64(c - 'a' + 10)
		case c >= 'A' && c <= 'F':
			val += uint64(c - 'A' + 10)
		default:
			return 0, fmt.Errorf("invalid hex character: %c", c)
		}
	}

	return val, nil
}

var ethNodeSyncingCmd = &cobra.Command{
	Use:   "syncing <network> <instance>",
	Short: "Get beacon node sync status",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		ctx := context.Background()

		pc, cleanup, err := startProxy(ctx)
		if err != nil {
			return err
		}

		defer cleanup()

		data, err := beaconGet(ctx, pc, args[0], args[1], "/eth/v1/node/syncing")
		if err != nil {
			return err
		}

		if ethnodeJSON {
			return printJSONBytes(data)
		}

		var resp struct {
			Data struct {
				HeadSlot     string `json:"head_slot"`
				SyncDistance string `json:"sync_distance"`
				IsSyncing    bool   `json:"is_syncing"`
				IsOptimistic bool   `json:"is_optimistic"`
				ELOffline    bool   `json:"el_offline"`
			} `json:"data"`
		}

		if err := json.Unmarshal(data, &resp); err != nil {
			return printJSONBytes(data)
		}

		fmt.Printf("Head slot:     %s\n", resp.Data.HeadSlot)
		fmt.Printf("Sync distance: %s\n", resp.Data.SyncDistance)
		fmt.Printf("Is syncing:    %v\n", resp.Data.IsSyncing)
		fmt.Printf("Is optimistic: %v\n", resp.Data.IsOptimistic)
		fmt.Printf("EL offline:    %v\n", resp.Data.ELOffline)

		return nil
	},
}

var ethNodeVersionCmd = &cobra.Command{
	Use:   "version <network> <instance>",
	Short: "Get node software version",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		ctx := context.Background()

		pc, cleanup, err := startProxy(ctx)
		if err != nil {
			return err
		}

		defer cleanup()

		data, err := beaconGet(ctx, pc, args[0], args[1], "/eth/v1/node/version")
		if err != nil {
			return err
		}

		if ethnodeJSON {
			return printJSONBytes(data)
		}

		var resp struct {
			Data struct {
				Version string `json:"version"`
			} `json:"data"`
		}

		if err := json.Unmarshal(data, &resp); err != nil {
			return printJSONBytes(data)
		}

		fmt.Printf("CL: %s\n", resp.Data.Version)

		// Also get EL version.
		elResult, err := execRPC(ctx, pc, args[0], args[1], "web3_clientVersion", nil)
		if err != nil {
			_, _ = fmt.Fprintf(log.Writer(), "EL: (error: %v)\n", err)

			return nil
		}

		var elVersion string
		if err := json.Unmarshal(elResult, &elVersion); err != nil {
			fmt.Printf("EL: %s\n", string(elResult))
		} else {
			fmt.Printf("EL: %s\n", elVersion)
		}

		return nil
	},
}

var ethNodeHealthCmd = &cobra.Command{
	Use:   "health <network> <instance>",
	Short: "Get beacon node health status",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		ctx := context.Background()

		pc, cleanup, err := startProxy(ctx)
		if err != nil {
			return err
		}

		defer cleanup()

		_, status, err := proxyDo(ctx, pc, "GET",
			fmt.Sprintf("/beacon/%s/%s/eth/v1/node/health", args[0], args[1]),
			nil, nil, nil)
		if err != nil {
			return err
		}

		if ethnodeJSON {
			return printJSON(map[string]any{"status_code": status})
		}

		switch status {
		case 200:
			fmt.Println("Healthy (200)")
		case 206:
			fmt.Println("Syncing (206)")
		case 503:
			fmt.Println("Not initialized (503)")
		default:
			fmt.Printf("Status: %d\n", status)
		}

		return nil
	},
}

var ethNodePeersCmd = &cobra.Command{
	Use:   "peers <network> <instance>",
	Short: "Get peer count",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		ctx := context.Background()

		pc, cleanup, err := startProxy(ctx)
		if err != nil {
			return err
		}

		defer cleanup()

		data, err := beaconGet(ctx, pc, args[0], args[1], "/eth/v1/node/peer_count")
		if err != nil {
			return err
		}

		if ethnodeJSON {
			return printJSONBytes(data)
		}

		var resp struct {
			Data struct {
				Connected     string `json:"connected"`
				Disconnected  string `json:"disconnected"`
				Connecting    string `json:"connecting"`
				Disconnecting string `json:"disconnecting"`
			} `json:"data"`
		}

		if err := json.Unmarshal(data, &resp); err != nil {
			return printJSONBytes(data)
		}

		fmt.Printf("Connected:     %s\n", resp.Data.Connected)
		fmt.Printf("Disconnected:  %s\n", resp.Data.Disconnected)
		fmt.Printf("Connecting:    %s\n", resp.Data.Connecting)
		fmt.Printf("Disconnecting: %s\n", resp.Data.Disconnecting)

		return nil
	},
}

var ethNodeFinalityCmd = &cobra.Command{
	Use:   "finality <network> <instance>",
	Short: "Get finality checkpoints",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		ctx := context.Background()

		pc, cleanup, err := startProxy(ctx)
		if err != nil {
			return err
		}

		defer cleanup()

		data, err := beaconGet(ctx, pc, args[0], args[1], "/eth/v1/beacon/states/head/finality_checkpoints")
		if err != nil {
			return err
		}

		if ethnodeJSON {
			return printJSONBytes(data)
		}

		var resp struct {
			Data struct {
				Finalized         struct{ Epoch string } `json:"finalized"`
				CurrentJustified  struct{ Epoch string } `json:"current_justified"`
				PreviousJustified struct{ Epoch string } `json:"previous_justified"`
			} `json:"data"`
		}

		if err := json.Unmarshal(data, &resp); err != nil {
			return printJSONBytes(data)
		}

		fmt.Printf("Finalized:          epoch %s\n", resp.Data.Finalized.Epoch)
		fmt.Printf("Current justified:  epoch %s\n", resp.Data.CurrentJustified.Epoch)
		fmt.Printf("Previous justified: epoch %s\n", resp.Data.PreviousJustified.Epoch)

		return nil
	},
}

var ethNodeHeaderCmd = &cobra.Command{
	Use:   "header <network> <instance> [slot]",
	Short: "Get beacon block header",
	Args:  cobra.RangeArgs(2, 3),
	RunE: func(_ *cobra.Command, args []string) error {
		ctx := context.Background()

		pc, cleanup, err := startProxy(ctx)
		if err != nil {
			return err
		}

		defer cleanup()

		slot := "head"
		if len(args) == 3 {
			slot = args[2]
		}

		data, err := beaconGet(ctx, pc, args[0], args[1], fmt.Sprintf("/eth/v1/beacon/headers/%s", slot))
		if err != nil {
			return err
		}

		if ethnodeJSON {
			return printJSONBytes(data)
		}

		var resp struct {
			Data struct {
				Root   string `json:"root"`
				Header struct {
					Message struct {
						Slot          string `json:"slot"`
						ProposerIndex string `json:"proposer_index"`
						ParentRoot    string `json:"parent_root"`
						StateRoot     string `json:"state_root"`
						BodyRoot      string `json:"body_root"`
					} `json:"message"`
				} `json:"header"`
			} `json:"data"`
		}

		if err := json.Unmarshal(data, &resp); err != nil {
			return printJSONBytes(data)
		}

		msg := resp.Data.Header.Message
		fmt.Printf("Slot:           %s\n", msg.Slot)
		fmt.Printf("Proposer index: %s\n", msg.ProposerIndex)
		fmt.Printf("Root:           %s\n", resp.Data.Root)
		fmt.Printf("Parent root:    %s\n", msg.ParentRoot)
		fmt.Printf("State root:     %s\n", msg.StateRoot)
		fmt.Printf("Body root:      %s\n", msg.BodyRoot)

		return nil
	},
}

var ethNodeBlockNumberCmd = &cobra.Command{
	Use:   "block-number <network> <instance>",
	Short: "Get latest execution layer block number",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		ctx := context.Background()

		pc, cleanup, err := startProxy(ctx)
		if err != nil {
			return err
		}

		defer cleanup()

		result, err := execRPC(ctx, pc, args[0], args[1], "eth_blockNumber", nil)
		if err != nil {
			return err
		}

		if ethnodeJSON {
			var hex string
			if err := json.Unmarshal(result, &hex); err != nil {
				return printJSONBytes(result)
			}

			num, err := hexToUint64(hex)
			if err != nil {
				return printJSONBytes(result)
			}

			return printJSON(map[string]any{"block_number": num, "hex": hex})
		}

		var hex string
		if err := json.Unmarshal(result, &hex); err != nil {
			fmt.Println(string(result))

			return nil
		}

		num, err := hexToUint64(hex)
		if err != nil {
			fmt.Println(hex)

			return nil
		}

		fmt.Println(num)

		return nil
	},
}

var ethNodeBeaconGetCmd = &cobra.Command{
	Use:   "beacon-get <network> <instance> <path>",
	Short: "GET any beacon API endpoint",
	Long: `Make a GET request to any beacon API endpoint.
The path should start with / (e.g., /eth/v1/node/identity).

Examples:
  ep ethnode beacon-get my-devnet lighthouse-geth-1 /eth/v1/node/identity
  ep ethnode beacon-get my-devnet lighthouse-geth-1 /eth/v1/config/deposit_contract`,
	Args: cobra.ExactArgs(3),
	RunE: func(_ *cobra.Command, args []string) error {
		ctx := context.Background()

		pc, cleanup, err := startProxy(ctx)
		if err != nil {
			return err
		}

		defer cleanup()

		path := args[2]
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}

		data, err := beaconGet(ctx, pc, args[0], args[1], path)
		if err != nil {
			return err
		}

		return printJSONBytes(data)
	},
}

var ethNodeExecRPCCmd = &cobra.Command{
	Use:   "exec-rpc <network> <instance> <method> [params-json]",
	Short: "Call any JSON-RPC method on an execution node",
	Long: `Make a JSON-RPC call to any execution node method.
Params should be a JSON array string.

Examples:
  ep ethnode exec-rpc my-devnet lighthouse-geth-1 eth_blockNumber
  ep ethnode exec-rpc my-devnet lighthouse-geth-1 eth_getBlockByNumber '["latest", false]'
  ep ethnode exec-rpc my-devnet lighthouse-geth-1 eth_chainId`,
	Args: cobra.RangeArgs(3, 4),
	RunE: func(_ *cobra.Command, args []string) error {
		ctx := context.Background()

		pc, cleanup, err := startProxy(ctx)
		if err != nil {
			return err
		}

		defer cleanup()

		var params []any
		if len(args) == 4 {
			if err := json.Unmarshal([]byte(args[3]), &params); err != nil {
				return fmt.Errorf("invalid params JSON (must be an array): %w", err)
			}
		}

		result, err := execRPC(ctx, pc, args[0], args[1], args[2], params)
		if err != nil {
			return err
		}

		return printJSONBytes(result)
	},
}
