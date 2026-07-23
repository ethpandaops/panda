package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// buildoorTokenEnv is the fallback source for the bearer token that buildoor
// verifies against the devnet's authenticatoor service.
const buildoorTokenEnv = "PANDA_BUILDOOR_TOKEN"

var buildoorCmd = &cobra.Command{
	GroupID: groupDirect,
	Use:     "buildoor",
	Short:   "Drive buildoor per-slot action plans",
	Long: `Inspect buildoor builder instances and script their per-slot action plans:
hot-patch payloads with jq transforms, review planned slots, and check what
actually happened.

A network's buildoor service URL (from cartographoor) is the multi-instance
overview; commands address one instance by its short name (see 'instances').

Slot arguments accept absolute numbers or offsets relative to the instance's
current slot (+N ahead, +-N behind). Plans freeze ~1 slot ahead — target
slots at least 2 ahead.

Mutations are credentialed by the panda proxy when it advertises buildoor
(the hosted proxy mints devnet tokens itself — no flags needed). Passing a
personal authenticatoor bearer token instead (--token or ` + buildoorTokenEnv + `,
minted at https://auth.<network>.ethpandaops.io/auth/token) goes direct and
keeps per-user attribution in buildoor's audit log.

Examples:
  panda buildoor networks
  panda buildoor instances glamsterdam-devnet-7
  panda buildoor test-transform glamsterdam-devnet-7 prysm-ethrex-1 payload '.gas_limit = 300000000'
  panda buildoor transform glamsterdam-devnet-7 prysm-ethrex-1 --slots +2,+3 --payload '.gas_limit = 300000000'
  panda buildoor plan glamsterdam-devnet-7 prysm-ethrex-1
  panda buildoor results glamsterdam-devnet-7 prysm-ethrex-1 --min-slot 1200 --max-slot 1210`,
}

func init() {
	rootCmd.AddCommand(buildoorCmd)

	buildoorCmd.AddCommand(
		buildoorNetworksCmd,
		buildoorInstancesCmd,
		buildoorOverviewCmd,
		buildoorPlanCmd,
		buildoorResultsCmd,
		buildoorTransformCmd,
		buildoorTestTransformCmd,
		buildoorPlanUpdateCmd,
	)

	completeBuildoorNetworks := completeOperationNetworkNames("buildoor.list_networks")
	for _, cmd := range buildoorCmd.Commands() {
		if cmd != buildoorNetworksCmd {
			cmd.ValidArgsFunction = completeBuildoorNetworks
		}
	}

	buildoorPlanCmd.Flags().String("min-slot", "", "range start slot, inclusive (absolute or +N; default: current-8)")
	buildoorPlanCmd.Flags().String("max-slot", "", "range end slot, inclusive (absolute or +N; default: current+24)")

	buildoorResultsCmd.Flags().String("min-slot", "", "range start slot, inclusive (absolute or +N)")
	buildoorResultsCmd.Flags().String("max-slot", "", "range end slot, inclusive (absolute or +N)")
	_ = buildoorResultsCmd.MarkFlagRequired("min-slot")
	_ = buildoorResultsCmd.MarkFlagRequired("max-slot")

	buildoorTransformCmd.Flags().StringSlice("slots", nil, "target slots (absolute or +N, comma-separated)")
	buildoorTransformCmd.Flags().String("from", "", "target range start slot, inclusive (absolute or +N)")
	buildoorTransformCmd.Flags().String("to", "", "target range end slot, inclusive (absolute or +N)")
	buildoorTransformCmd.Flags().String("payload", "", "jq expression rewriting the built execution payload ('' clears it)")
	buildoorTransformCmd.Flags().String("bid", "", "jq expression rewriting the bid message before re-signing ('' clears it)")
	buildoorTransformCmd.Flags().String("envelope", "", "jq expression rewriting the envelope message before re-signing ('' clears it)")
	buildoorTransformCmd.Flags().Bool("clear", false, "remove all transforms from the target slots")
	buildoorTransformCmd.Flags().String("token", "", "authenticatoor bearer token (default: $"+buildoorTokenEnv+")")

	buildoorTestTransformCmd.Flags().Uint64("sample-slot", 0, "run against this slot's captured artifact instead of a template")

	buildoorPlanUpdateCmd.Flags().String("updates", "", "PlanUpdate JSON array, passed through verbatim")
	buildoorPlanUpdateCmd.Flags().String("token", "", "authenticatoor bearer token (default: $"+buildoorTokenEnv+")")
	_ = buildoorPlanUpdateCmd.MarkFlagRequired("updates")
}

var buildoorNetworksCmd = &cobra.Command{
	Use:   "networks",
	Short: "List networks with buildoor deployments",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		response, err := runServerOperation(cmd, "buildoor.list_networks", map[string]any{})
		if err != nil {
			return err
		}

		return printListing(response, "networks", "No networks with buildoor deployments found.")
	},
}

var buildoorInstancesCmd = &cobra.Command{
	Use:   "instances <network>",
	Short: "List a network's buildoor instances",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		response, err := runServerOperation(cmd, "buildoor.list_instances", map[string]any{
			"network": args[0],
		})
		if err != nil {
			return err
		}

		return printListing(response, "instances", "No buildoor instances found.")
	},
}

var buildoorOverviewCmd = &cobra.Command{
	Use:   "overview <network> <instance>",
	Short: "Get an instance's status overview (always JSON)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		response, err := runServerOperationRaw(cmd, "buildoor.get_overview", map[string]any{
			"network":  args[0],
			"instance": args[1],
		})
		if err != nil {
			return err
		}

		return printJSONBytes(response.Body)
	},
}

var buildoorPlanCmd = &cobra.Command{
	Use:   "plan <network> <instance>",
	Short: "Get per-slot action plans (always JSON)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		minSlot, _ := cmd.Flags().GetString("min-slot")
		maxSlot, _ := cmd.Flags().GetString("max-slot")

		if (minSlot == "") != (maxSlot == "") {
			return fmt.Errorf("--min-slot and --max-slot must be provided together")
		}

		if minSlot == "" {
			minSlot, maxSlot = "+-8", "+24"
		}

		return buildoorSlotRangeQuery(cmd, "buildoor.get_action_plan", args[0], args[1], minSlot, maxSlot)
	},
}

var buildoorResultsCmd = &cobra.Command{
	Use:   "results <network> <instance>",
	Short: "Get per-slot outcome history (always JSON)",
	Long: `Get the recorded outcome history — build, bids, block submissions, reveals,
inclusion, and the frozen applied plan — for every active slot in the range
(always JSON).`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		minSlot, _ := cmd.Flags().GetString("min-slot")
		maxSlot, _ := cmd.Flags().GetString("max-slot")

		return buildoorSlotRangeQuery(cmd, "buildoor.get_slot_results", args[0], args[1], minSlot, maxSlot)
	},
}

var buildoorTransformCmd = &cobra.Command{
	Use:   "transform <network> <instance>",
	Short: "Set jq transforms on future slots",
	Long: `Set the action plan's jq transforms on future slots, hot-patching builder
objects for testing. Expressions run against the object's JSON form:
  --payload   rewrites the built execution payload before it feeds the bid
              commitment and the reveal
  --bid       rewrites the bid message just before signing (then re-signed)
  --envelope  rewrites the envelope message just before signing (then re-signed)

Plans freeze ~1 slot ahead, so target slots at least 2 ahead (e.g. --slots +2).
An empty expression ('') clears that one transform; --clear removes all three.

Examples:
  panda buildoor transform glamsterdam-devnet-7 prysm-ethrex-1 --slots +2,+3 --payload '.gas_limit = 300000000'
  panda buildoor transform glamsterdam-devnet-7 prysm-ethrex-1 --from +2 --to +10 --payload '.gas_limit = 300000000'
  panda buildoor transform glamsterdam-devnet-7 prysm-ethrex-1 --slots +2 --clear`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		network, instance := args[0], args[1]

		clearAll, _ := cmd.Flags().GetBool("clear")
		set := make(map[string]any, 3)

		for _, target := range []string{"payload", "bid", "envelope"} {
			if cmd.Flags().Changed(target) {
				expr, _ := cmd.Flags().GetString(target)
				set["transforms."+target] = expr
			}
		}

		if clearAll && len(set) > 0 {
			return fmt.Errorf("--clear cannot be combined with transform expressions")
		}

		if !clearAll && len(set) == 0 {
			return fmt.Errorf("provide at least one of --payload, --bid, --envelope, or --clear")
		}

		update, err := buildoorTargetSlots(cmd, network, instance)
		if err != nil {
			return err
		}

		if clearAll {
			update["transforms"] = nil
		} else {
			update["set"] = set
		}

		return buildoorApplyUpdates(cmd, network, instance, []any{update})
	},
}

var buildoorTestTransformCmd = &cobra.Command{
	Use:   "test-transform <network> <instance> <target> <expression>",
	Short: "Evaluate a jq transform against a sample object",
	Long: `Evaluate a jq expression against a sample builder object on the instance
without touching any plan. Target is payload, bid, or envelope. The input is
the slot's captured artifact when --sample-slot is given and available,
otherwise a template.

Example:
  panda buildoor test-transform glamsterdam-devnet-7 prysm-ethrex-1 payload '.gas_limit = 300000000'`,
	Args: cobra.ExactArgs(4),
	RunE: func(cmd *cobra.Command, args []string) error {
		opArgs := map[string]any{
			"network":    args[0],
			"instance":   args[1],
			"target":     args[2],
			"expression": args[3],
		}

		if sampleSlot, _ := cmd.Flags().GetUint64("sample-slot"); sampleSlot > 0 {
			opArgs["sample_slot"] = sampleSlot
		}

		response, err := runServerOperationRaw(cmd, "buildoor.test_transform", opArgs)
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSONBytes(response.Body)
		}

		var result struct {
			InputSource string `json:"input_source"`
			Output      string `json:"output"`
			Error       string `json:"error"`
		}

		if err := json.Unmarshal(response.Body, &result); err != nil {
			return fmt.Errorf("decoding response: %w", err)
		}

		if result.Error != "" {
			return fmt.Errorf("transform failed against %s input: %s", result.InputSource, result.Error)
		}

		fmt.Printf("Input: %s\n%s\n", result.InputSource, result.Output)

		return nil
	},
}

var buildoorPlanUpdateCmd = &cobra.Command{
	Use:   "plan-update <network> <instance>",
	Short: "Apply raw PlanUpdate JSON to the action plan",
	Long: `Apply a raw bulk action-plan mutation, exposing the full PlanUpdate schema
(bid/builder_api/reveal/build categories, fine-grained set paths) beyond the
transform shortcuts. See buildoor's POST /api/buildoor/action-plan docs.

Example:
  panda buildoor plan-update glamsterdam-devnet-7 prysm-ethrex-1 \
    --updates '[{"slots":[1234],"set":{"bid.bid_value_gwei":5000}}]'`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		raw, _ := cmd.Flags().GetString("updates")

		var updates []any
		if err := json.Unmarshal([]byte(raw), &updates); err != nil {
			return fmt.Errorf("--updates must be a JSON array: %w", err)
		}

		return buildoorApplyUpdates(cmd, args[0], args[1], updates)
	},
}

// buildoorSlotRangeQuery resolves the (possibly relative) slot range and runs
// a range-scoped read operation, printing the raw JSON response.
func buildoorSlotRangeQuery(cmd *cobra.Command, operationID, network, instance, minSlot, maxSlot string) error {
	resolve := buildoorSlotResolver(cmd, network, instance)

	minResolved, err := resolve(minSlot)
	if err != nil {
		return err
	}

	maxResolved, err := resolve(maxSlot)
	if err != nil {
		return err
	}

	response, err := runServerOperationRaw(cmd, operationID, map[string]any{
		"network":  network,
		"instance": instance,
		"min_slot": minResolved,
		"max_slot": maxResolved,
	})
	if err != nil {
		return err
	}

	return printJSONBytes(response.Body)
}

// buildoorTargetSlots builds the update's slot targeting from --slots or
// --from/--to, resolving relative values.
func buildoorTargetSlots(cmd *cobra.Command, network, instance string) (map[string]any, error) {
	slots, _ := cmd.Flags().GetStringSlice("slots")
	from, _ := cmd.Flags().GetString("from")
	to, _ := cmd.Flags().GetString("to")

	if (from == "") != (to == "") {
		return nil, fmt.Errorf("--from and --to must be provided together")
	}

	if len(slots) == 0 && from == "" {
		return nil, fmt.Errorf("provide target slots via --slots or --from/--to")
	}

	resolve := buildoorSlotResolver(cmd, network, instance)
	update := make(map[string]any, 3)

	if len(slots) > 0 {
		resolved := make([]uint64, 0, len(slots))

		for _, slot := range slots {
			value, err := resolve(slot)
			if err != nil {
				return nil, err
			}

			resolved = append(resolved, value)
		}

		update["slots"] = resolved
	}

	if from != "" {
		fromResolved, err := resolve(from)
		if err != nil {
			return nil, err
		}

		toResolved, err := resolve(to)
		if err != nil {
			return nil, err
		}

		update["from_slot"] = fromResolved
		update["to_slot"] = toResolved
	}

	return update, nil
}

// buildoorSlotResolver parses slot specs: plain numbers pass through, +N (and
// +-N) resolve against the instance's current slot, fetched once on first use.
func buildoorSlotResolver(cmd *cobra.Command, network, instance string) func(string) (uint64, error) {
	currentSlot := int64(-1)

	return func(spec string) (uint64, error) {
		offset, relative := strings.CutPrefix(spec, "+")
		if !relative {
			value, err := strconv.ParseUint(spec, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid slot %q: expected a slot number or +N", spec)
			}

			return value, nil
		}

		delta, err := strconv.ParseInt(offset, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid relative slot %q: expected +N", spec)
		}

		if currentSlot < 0 {
			slot, err := buildoorCurrentSlot(cmd, network, instance)
			if err != nil {
				return 0, err
			}

			currentSlot = slot
		}

		resolved := currentSlot + delta
		if resolved < 0 {
			resolved = 0
		}

		return uint64(resolved), nil
	}
}

// buildoorCurrentSlot reads the instance's current slot from its overview.
func buildoorCurrentSlot(cmd *cobra.Command, network, instance string) (int64, error) {
	response, err := runServerOperationRaw(cmd, "buildoor.get_overview", map[string]any{
		"network":  network,
		"instance": instance,
	})
	if err != nil {
		return 0, err
	}

	var overview struct {
		CurrentSlot int64 `json:"current_slot"`
	}

	if err := json.Unmarshal(response.Body, &overview); err != nil {
		return 0, fmt.Errorf("decoding instance overview: %w", err)
	}

	if overview.CurrentSlot <= 0 {
		return 0, fmt.Errorf(
			"instance %q reports current_slot=%d — cannot resolve relative slots, pass absolute slot numbers",
			instance, overview.CurrentSlot,
		)
	}

	return overview.CurrentSlot, nil
}

// buildoorApplyUpdates runs the mutation and prints the authoritative result.
// Without an explicit token the server routes the mutation through a proxy
// that advertises buildoor (the proxy mints the devnet credential); a token
// forces the direct path and keeps per-user attribution in buildoor's audit.
func buildoorApplyUpdates(cmd *cobra.Command, network, instance string, updates []any) error {
	token, _ := cmd.Flags().GetString("token")
	if token == "" {
		token = strings.TrimSpace(os.Getenv(buildoorTokenEnv))
	}

	args := map[string]any{
		"network":  network,
		"instance": instance,
		"updates":  updates,
	}
	if token != "" {
		args["auth_token"] = token
	}

	response, err := runServerOperationRaw(cmd, "buildoor.update_action_plan", args)
	if err != nil {
		return err
	}

	if isJSON() {
		return printJSONBytes(response.Body)
	}

	var result struct {
		Status string   `json:"status"`
		Slots  []uint64 `json:"slots"`
	}

	if err := json.Unmarshal(response.Body, &result); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	fmt.Printf("Updated %d slot plan(s): %v\n", len(result.Slots), result.Slots)

	return nil
}
