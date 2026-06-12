package cli

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/spf13/cobra"
)

var tracoorCmd = &cobra.Command{
	GroupID: groupDirect,
	Use:     "tracoor",
	Short:   "Query Tracoor forensics archives: beacon states, bad blocks/blobs, execution traces",
	Long: `Query Tracoor, the forensics archive that captures beacon states and
blocks, invalid ("bad") beacon blocks and blob sidecars rejected from
gossip, execution debug traces, and invalid execution blocks from a fleet
of nodes.

Listings return capture metadata; raw artifacts (SSZ/JSON, possibly gzip)
are served from the URL printed by 'panda tracoor url'.

Artifact types: beacon_state, beacon_block, beacon_bad_block,
beacon_bad_blob, execution_block_trace, execution_bad_block.

Examples:
  panda tracoor networks
  panda tracoor states mainnet --slot 14535165
  panda tracoor bad-blocks mainnet --after 2026-06-01T00:00:00Z
  panda tracoor bad-blocks mainnet --count
  panda tracoor traces mainnet --block-number 23000000
  panda tracoor unique mainnet beacon_state node beacon_implementation
  panda tracoor url mainnet beacon_state <id>
  panda tracoor link mainnet beacon_bad_block`,
}

// tracoorListFlags holds the per-command flag values for an artifact
// listing command.
type tracoorListFlags struct {
	node           string
	slot           int64
	epoch          int64
	root           string
	blockHash      string
	blockNumber    int64
	index          int64
	extraData      string
	nodeVersion    string
	implementation string
	before         string
	after          string
	id             string
	offset         int
	limit          int
	orderBy        string
	count          bool
}

// tracoorListSpec describes one artifact listing subcommand.
type tracoorListSpec struct {
	use      string
	short    string
	artifact string
	// rootFlag is the filter key behind --root (state_root or block_root);
	// empty for execution artifacts, which filter by hash/number instead.
	rootFlag       string
	execution      bool
	indexFlag      bool
	extraDataFlag  bool
	implementation string
}

var tracoorListSpecs = []tracoorListSpec{
	{
		use: "states <network>", short: "List captured beacon states (SSZ snapshots)",
		artifact: "beacon_state", rootFlag: "state_root", implementation: "beacon_implementation",
	},
	{
		use: "blocks <network>", short: "List captured beacon blocks",
		artifact: "beacon_block", rootFlag: "block_root", implementation: "beacon_implementation",
	},
	{
		use: "bad-blocks <network>", short: "List invalid beacon blocks rejected from gossip",
		artifact: "beacon_bad_block", rootFlag: "block_root", implementation: "beacon_implementation",
	},
	{
		use: "bad-blobs <network>", short: "List invalid blob sidecars rejected from gossip",
		artifact: "beacon_bad_blob", rootFlag: "block_root", indexFlag: true, implementation: "beacon_implementation",
	},
	{
		use: "traces <network>", short: "List execution debug_traceBlock captures",
		artifact: "execution_block_trace", execution: true, implementation: "execution_implementation",
	},
	{
		use: "bad-execution-blocks <network>", short: "List invalid execution blocks (debug_getBadBlocks)",
		artifact: "execution_bad_block", execution: true, extraDataFlag: true, implementation: "execution_implementation",
	},
}

func init() {
	rootCmd.AddCommand(tracoorCmd)

	tracoorCmd.AddCommand(
		tracoorNetworksCmd,
		tracoorConfigCmd,
		tracoorUniqueCmd,
		tracoorURLCmd,
		tracoorLinkCmd,
	)

	for _, spec := range tracoorListSpecs {
		tracoorCmd.AddCommand(newTracoorListCommand(spec))
	}

	for _, cmd := range []*cobra.Command{
		tracoorConfigCmd,
		tracoorUniqueCmd,
		tracoorURLCmd,
		tracoorLinkCmd,
	} {
		cmd.ValidArgsFunction = completeTracoorNetworkNames
	}
}

var tracoorNetworksCmd = &cobra.Command{
	Use:   "networks",
	Short: "List networks with Tracoor instances",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		response, err := runServerOperation(cmd, "tracoor.list_networks", map[string]any{})
		if err != nil {
			return err
		}

		return printListing(response, "networks", "No networks with Tracoor instances found.")
	},
}

var tracoorConfigCmd = &cobra.Command{
	Use:   "config <network>",
	Short: "Show the instance's Ethereum network-config and tool settings (always JSON)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		response, err := runServerOperationRaw(cmd, "tracoor.get_config", map[string]any{
			"network": args[0],
		})
		if err != nil {
			return err
		}

		return printJSONBytes(response.Body)
	},
}

var tracoorUniqueCmd = &cobra.Command{
	Use:   "unique <network> <artifact> <field>...",
	Short: "List distinct values for fields of an artifact type (e.g. node, beacon_implementation)",
	Args:  cobra.MinimumNArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		fields := make([]any, 0, len(args)-2)
		for _, field := range args[2:] {
			fields = append(fields, field)
		}

		body, err := runTracoorPassthrough(cmd, "tracoor.list_unique_values", map[string]any{
			"network":  args[0],
			"artifact": args[1],
			"fields":   fields,
		})
		if err != nil || body == nil {
			return err
		}

		keys := make([]string, 0, len(body))
		for key, value := range body {
			if values, ok := value.([]any); ok && len(values) > 0 {
				keys = append(keys, key)
			}
		}

		sort.Strings(keys)

		if len(keys) == 0 {
			fmt.Println("No values found.")
			return nil
		}

		for _, key := range keys {
			values, _ := body[key].([]any)
			fmt.Printf("%s:\n", key)
			for _, value := range values {
				fmt.Printf("  %v\n", value)
			}
		}

		return nil
	},
}

var tracoorURLCmd = &cobra.Command{
	Use:   "url <network> <artifact> <id>",
	Short: "Print the raw artifact download URL for a capture (SSZ/JSON, possibly gzip)",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTracoorURLOperation(cmd, "tracoor.download_url", map[string]any{
			"network":  args[0],
			"artifact": args[1],
			"id":       args[2],
		})
	},
}

var tracoorLinkCmd = &cobra.Command{
	Use:   "link <network> <artifact> [id]",
	Short: "Print a deep link to an artifact listing or capture in the Tracoor web UI",
	Args:  cobra.RangeArgs(2, 3),
	RunE: func(cmd *cobra.Command, args []string) error {
		opArgs := map[string]any{"network": args[0], "artifact": args[1]}
		if len(args) == 3 {
			opArgs["id"] = args[2]
		}

		return runTracoorURLOperation(cmd, "tracoor.link_artifact", opArgs)
	},
}

// newTracoorListCommand builds one artifact listing subcommand from its spec.
func newTracoorListCommand(spec tracoorListSpec) *cobra.Command {
	flags := &tracoorListFlags{}

	cmd := &cobra.Command{
		Use:   spec.use,
		Short: spec.short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opArgs := tracoorListArgs(spec, flags, args[0])

			if flags.count {
				response, err := runServerOperation(cmd, "tracoor.count_artifacts", opArgs)
				if err != nil {
					return err
				}

				if isJSON() {
					return printJSON(response)
				}

				data, _ := response.Data.(map[string]any)
				fmt.Printf("%v\n", formatJSONNumber(data["count"]))

				return nil
			}

			opArgs["offset"] = flags.offset
			opArgs["limit"] = flags.limit
			setIfNotEmpty(opArgs, "order_by", flags.orderBy)
			setIfNotEmpty(opArgs, "id", flags.id)

			body, err := runTracoorPassthrough(cmd, "tracoor.list_artifacts", opArgs)
			if err != nil || body == nil {
				return err
			}

			printTracoorListing(spec, body)

			return nil
		},
	}

	cmd.ValidArgsFunction = completeTracoorNetworkNames

	cmd.Flags().StringVar(&flags.node, "node", "", "Filter by node name")
	cmd.Flags().StringVar(&flags.nodeVersion, "node-version", "", "Filter by node version")
	cmd.Flags().StringVar(&flags.implementation, "implementation", "", "Filter by client implementation (e.g. lighthouse, geth)")
	cmd.Flags().StringVar(&flags.before, "before", "", "Only captures fetched at or before this RFC 3339 timestamp")
	cmd.Flags().StringVar(&flags.after, "after", "", "Only captures fetched at or after this RFC 3339 timestamp")
	cmd.Flags().StringVar(&flags.id, "id", "", "Fetch a single capture by ID")
	cmd.Flags().IntVar(&flags.offset, "offset", 0, "Pagination offset")
	cmd.Flags().IntVar(&flags.limit, "limit", 100, "Maximum captures to return")
	cmd.Flags().StringVar(&flags.orderBy, "order", "", `Sort order (default "fetched_at DESC")`)
	cmd.Flags().BoolVar(&flags.count, "count", false, "Print only the number of matching captures")

	if spec.execution {
		cmd.Flags().Int64Var(&flags.blockNumber, "block-number", -1, "Filter by execution block number")
		cmd.Flags().StringVar(&flags.blockHash, "block-hash", "", "Filter by execution block hash")
	} else {
		cmd.Flags().Int64Var(&flags.slot, "slot", -1, "Filter by slot")
		cmd.Flags().Int64Var(&flags.epoch, "epoch", -1, "Filter by epoch")
		cmd.Flags().StringVar(&flags.root, "root", "", "Filter by "+spec.rootFlag)
	}

	if spec.indexFlag {
		cmd.Flags().Int64Var(&flags.index, "index", -1, "Filter by blob index")
	}

	if spec.extraDataFlag {
		cmd.Flags().StringVar(&flags.extraData, "extra-data", "", "Filter by block extra-data string")
	}

	return cmd
}

// tracoorListArgs builds the shared filter args for list/count operations.
func tracoorListArgs(spec tracoorListSpec, flags *tracoorListFlags, network string) map[string]any {
	opArgs := map[string]any{"network": network, "artifact": spec.artifact}

	setIfNotEmpty(opArgs, "node", flags.node)
	setIfNotEmpty(opArgs, "node_version", flags.nodeVersion)
	setIfNotEmpty(opArgs, spec.implementation, flags.implementation)
	setIfNotEmpty(opArgs, "before", flags.before)
	setIfNotEmpty(opArgs, "after", flags.after)

	if spec.execution {
		if flags.blockNumber >= 0 {
			opArgs["block_number"] = flags.blockNumber
		}

		setIfNotEmpty(opArgs, "block_hash", flags.blockHash)
		setIfNotEmpty(opArgs, "block_extra_data", flags.extraData)
	} else {
		if flags.slot >= 0 {
			opArgs["slot"] = flags.slot
		}

		if flags.epoch >= 0 {
			opArgs["epoch"] = flags.epoch
		}

		setIfNotEmpty(opArgs, spec.rootFlag, flags.root)
	}

	if spec.indexFlag && flags.index >= 0 {
		opArgs["index"] = flags.index
	}

	return opArgs
}

// printTracoorListing renders the single-key listing body ({"beacon_states":
// [...]}-style) as one row per capture.
func printTracoorListing(spec tracoorListSpec, body map[string]any) {
	var items []any

	for _, value := range body {
		if list, ok := value.([]any); ok {
			items = list
			break
		}
	}

	if len(items) == 0 {
		fmt.Println("No captures found.")
		return
	}

	for _, item := range items {
		capture, _ := item.(map[string]any)
		if capture == nil {
			continue
		}

		if spec.execution {
			fmt.Printf("  block=%v  %v (%v)  hash=%v  id=%v  fetched_at=%v\n",
				capture["block_number"], capture["node"], capture["execution_implementation"],
				capture["block_hash"], capture["id"], capture["fetched_at"])
			continue
		}

		root := capture["state_root"]
		if root == nil {
			root = capture["block_root"]
		}

		extra := ""
		if index, ok := capture["index"]; ok {
			extra = fmt.Sprintf("  index=%v", index)
		}

		fmt.Printf("  slot=%v%s  %v (%v)  root=%v  id=%v  fetched_at=%v\n",
			capture["slot"], extra, capture["node"], capture["beacon_implementation"],
			root, capture["id"], capture["fetched_at"])
	}
}

// runTracoorURLOperation runs an operation whose payload is {"url": ...} and
// prints the URL.
func runTracoorURLOperation(cmd *cobra.Command, operationID string, args map[string]any) error {
	response, err := runServerOperation(cmd, operationID, args)
	if err != nil {
		return err
	}

	if isJSON() {
		return printJSON(response)
	}

	data, _ := response.Data.(map[string]any)
	fmt.Printf("%v\n", data["url"])

	return nil
}

// runTracoorPassthrough runs a Tracoor passthrough operation and decodes the
// JSON body for human-readable rendering. In JSON output mode it prints the
// raw body and returns (nil, nil) so callers skip rendering.
func runTracoorPassthrough(cmd *cobra.Command, operationID string, args map[string]any) (map[string]any, error) {
	response, err := runServerOperationRaw(cmd, operationID, args)
	if err != nil {
		return nil, err
	}

	if isJSON() {
		return nil, printJSONBytes(response.Body)
	}

	var body map[string]any
	if err := json.Unmarshal(response.Body, &body); err != nil {
		return nil, fmt.Errorf("decoding Tracoor response: %w", err)
	}

	return body, nil
}
