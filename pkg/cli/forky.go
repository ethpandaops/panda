package cli

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
)

var forkyCmd = &cobra.Command{
	GroupID: groupDirect,
	Use:     "forky",
	Short:   "Query Forky fork-choice snapshots",
	Long: `Query Forky for fork-choice snapshots (frames) captured from beacon nodes.

Examples:
  panda forky networks
  panda forky now mainnet
  panda forky spec mainnet
  panda forky frames mainnet --slot 7654321
  panda forky frames mainnet --event-source xatu_reorg_event --limit 10
  panda forky frame mainnet 0d2855c9-cf83-4b0a-9d83-041f25d39bd5
  panda forky nodes mainnet
  panda forky labels mainnet`,
}

// forkyFilterFlags holds the shared frame filter flags for listing commands.
type forkyFilterFlags struct {
	node            string
	slot            int64
	epoch           int64
	labels          []string
	consensusClient string
	eventSource     string
	before          string
	after           string
	offset          int
	limit           int
}

func init() {
	rootCmd.AddCommand(forkyCmd)

	forkyCmd.AddCommand(
		forkyNetworksCmd,
		forkyNowCmd,
		forkySpecCmd,
		forkyFramesCmd,
		forkyFrameCmd,
		forkyNodesCmd,
		forkySlotsCmd,
		forkyEpochsCmd,
		forkyLabelsCmd,
	)

	for _, cmd := range []*cobra.Command{
		forkyNowCmd,
		forkySpecCmd,
		forkyFramesCmd,
		forkyFrameCmd,
		forkyNodesCmd,
		forkySlotsCmd,
		forkyEpochsCmd,
		forkyLabelsCmd,
	} {
		cmd.ValidArgsFunction = completeForkyNetworkNames
	}
}

// registerForkyFilterFlags wires the shared frame filter flags onto a command.
func registerForkyFilterFlags(cmd *cobra.Command, flags *forkyFilterFlags) {
	cmd.Flags().StringVar(&flags.node, "node", "", "Filter by node name")
	cmd.Flags().Int64Var(&flags.slot, "slot", -1, "Filter by wall-clock slot")
	cmd.Flags().Int64Var(&flags.epoch, "epoch", -1, "Filter by wall-clock epoch")
	cmd.Flags().StringArrayVar(&flags.labels, "label", nil, "Filter by label (repeatable, all must match)")
	cmd.Flags().StringVar(&flags.consensusClient, "client", "", "Filter by consensus client (e.g. lighthouse)")
	cmd.Flags().StringVar(&flags.eventSource, "event-source", "",
		"Filter by event source (beacon_node, xatu_polling, xatu_reorg_event)")
	cmd.Flags().StringVar(&flags.before, "before", "", "Only frames fetched at or before this RFC 3339 time")
	cmd.Flags().StringVar(&flags.after, "after", "", "Only frames fetched at or after this RFC 3339 time")
	cmd.Flags().IntVar(&flags.offset, "offset", 0, "Pagination offset")
	cmd.Flags().IntVar(&flags.limit, "limit", 100, "Maximum results to return")
}

// operationArgs converts the filter flags into server operation args.
func (f *forkyFilterFlags) operationArgs(network string) map[string]any {
	args := map[string]any{
		"network": network,
		"offset":  f.offset,
		"limit":   f.limit,
	}

	if f.node != "" {
		args["node"] = f.node
	}
	if f.slot >= 0 {
		args["slot"] = f.slot
	}
	if f.epoch >= 0 {
		args["epoch"] = f.epoch
	}
	if len(f.labels) > 0 {
		args["labels"] = f.labels
	}
	if f.consensusClient != "" {
		args["consensus_client"] = f.consensusClient
	}
	if f.eventSource != "" {
		args["event_source"] = f.eventSource
	}
	if f.before != "" {
		args["before"] = f.before
	}
	if f.after != "" {
		args["after"] = f.after
	}

	return args
}

var forkyNetworksCmd = &cobra.Command{
	Use:   "networks",
	Short: "List networks with Forky instances",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		response, err := runServerOperation(cmd, "forky.list_networks", map[string]any{})
		if err != nil {
			return err
		}

		return printListing(response, "networks", "No networks with Forky instances found.")
	},
}

var forkyNowCmd = &cobra.Command{
	Use:   "now <network>",
	Short: "Get the network's current wall-clock slot and epoch",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		response, err := runServerOperation(cmd, "forky.get_now", map[string]any{
			"network": args[0],
		})
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSON(response)
		}

		data, _ := response.Data.(map[string]any)
		printKeyValue([][2]string{
			{"Network", args[0]},
			{"Slot", formatForkyValue(data["slot"])},
			{"Epoch", formatForkyValue(data["epoch"])},
		})

		return nil
	},
}

var forkySpecCmd = &cobra.Command{
	Use:   "spec <network>",
	Short: "Get the network's chain spec",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		response, err := runServerOperation(cmd, "forky.get_spec", map[string]any{
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
			{"Network", fmt.Sprintf("%v", data["network_name"])},
		}

		if spec, ok := data["spec"].(map[string]any); ok {
			for _, kv := range [][2]string{
				{"Seconds per slot", "seconds_per_slot"},
				{"Slots per epoch", "slots_per_epoch"},
				{"Genesis time", "genesis_time"},
			} {
				if value, ok := spec[kv[1]]; ok {
					pairs = append(pairs, [2]string{kv[0], fmt.Sprintf("%v", value)})
				}
			}
		}

		printKeyValue(pairs)

		return nil
	},
}

var forkyFramesFilter forkyFilterFlags

var forkyFramesCmd = &cobra.Command{
	Use:   "frames <network>",
	Short: "List fork-choice frame metadata",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		response, err := runServerOperation(cmd, "forky.list_frames", forkyFramesFilter.operationArgs(args[0]))
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSON(response)
		}

		data, _ := response.Data.(map[string]any)
		frames, _ := data["frames"].([]any)
		if len(frames) == 0 {
			fmt.Println("No frames found.")
			return nil
		}

		fmt.Printf("Frames (%v total):\n", formatForkyValue(data["total"]))
		for _, item := range frames {
			frame, _ := item.(map[string]any)
			fmt.Printf(
				"  %v  slot=%v  node=%v  client=%v  source=%v  fetched=%v\n",
				frame["id"],
				formatForkyValue(frame["wall_clock_slot"]),
				frame["node"],
				frame["consensus_client"],
				frame["event_source"],
				frame["fetched_at"],
			)
		}

		return nil
	},
}

var forkyFrameCmd = &cobra.Command{
	Use:   "frame <network> <frame-id>",
	Short: "Get a full fork-choice frame by ID (always JSON)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		response, err := runServerOperationRaw(cmd, "forky.get_frame", map[string]any{
			"network":  args[0],
			"frame_id": args[1],
		})
		if err != nil {
			return err
		}

		return printJSONBytes(response.Body)
	},
}

var forkyNodesFilter forkyFilterFlags

var forkyNodesCmd = &cobra.Command{
	Use:   "nodes <network>",
	Short: "List nodes with captured frames",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runForkyValueListing(cmd, "forky.list_nodes", "nodes", &forkyNodesFilter, args[0],
			"No nodes found.")
	},
}

var forkySlotsFilter forkyFilterFlags

var forkySlotsCmd = &cobra.Command{
	Use:   "slots <network>",
	Short: "List slots with captured frames",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runForkyValueListing(cmd, "forky.list_slots", "slots", &forkySlotsFilter, args[0],
			"No slots found.")
	},
}

var forkyEpochsFilter forkyFilterFlags

var forkyEpochsCmd = &cobra.Command{
	Use:   "epochs <network>",
	Short: "List epochs with captured frames",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runForkyValueListing(cmd, "forky.list_epochs", "epochs", &forkyEpochsFilter, args[0],
			"No epochs found.")
	},
}

var forkyLabelsFilter forkyFilterFlags

var forkyLabelsCmd = &cobra.Command{
	Use:   "labels <network>",
	Short: "List labels on captured frames",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runForkyValueListing(cmd, "forky.list_labels", "labels", &forkyLabelsFilter, args[0],
			"No labels found.")
	},
}

func init() {
	registerForkyFilterFlags(forkyFramesCmd, &forkyFramesFilter)
	registerForkyFilterFlags(forkyNodesCmd, &forkyNodesFilter)
	registerForkyFilterFlags(forkySlotsCmd, &forkySlotsFilter)
	registerForkyFilterFlags(forkyEpochsCmd, &forkyEpochsFilter)
	registerForkyFilterFlags(forkyLabelsCmd, &forkyLabelsFilter)
}

// runForkyValueListing runs a metadata value listing operation and prints the
// scalar results one per line.
func runForkyValueListing(
	cmd *cobra.Command,
	operationID, key string,
	flags *forkyFilterFlags,
	network, emptyMessage string,
) error {
	response, err := runServerOperation(cmd, operationID, flags.operationArgs(network))
	if err != nil {
		return err
	}

	if isJSON() {
		return printJSON(response)
	}

	data, _ := response.Data.(map[string]any)
	values, _ := data[key].([]any)
	if len(values) == 0 {
		fmt.Println(emptyMessage)
		return nil
	}

	for _, value := range values {
		fmt.Printf("%v\n", formatForkyValue(value))
	}

	return nil
}

// formatForkyValue renders a decoded JSON value, keeping integral float64
// numbers (slots, epochs, totals) in plain decimal notation instead of the
// scientific form %v would use.
func formatForkyValue(value any) string {
	number, ok := value.(float64)
	if !ok {
		return fmt.Sprintf("%v", value)
	}

	if number == float64(int64(number)) {
		return strconv.FormatInt(int64(number), 10)
	}

	return strconv.FormatFloat(number, 'f', -1, 64)
}
