package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var rolloutCmd = &cobra.Command{
	GroupID: groupDirect,
	Use:     "rollout",
	Short:   "See and steer rolloor client rollouts on devnets",
	Long: `See and steer the client rollouts rolloor runs on a devnet. Only devnets
that run rolloor are listed; reading needs no login, acting needs
'panda auth login' and the rights rolloor gives you on that devnet.

Examples:
  panda rollout networks
  panda rollout status glamsterdam-devnet-12
  panda rollout list glamsterdam-devnet-12
  panda rollout show glamsterdam-devnet-12 6cb3b0cd89ed
  panda rollout history glamsterdam-devnet-12 --group lighthouse
  panda rollout sync glamsterdam-devnet-12 client=lighthouse
  panda rollout pause glamsterdam-devnet-12 6cb3b0cd89ed
  panda rollout suspend glamsterdam-devnet-12 node=lighthouse-geth-1 --reason "debugging" --for 4h`,
}

var (
	rolloutHistoryGroup    string
	rolloutHistorySelector string
	rolloutHistoryLimit    int
	rolloutSyncStrategy    string
	rolloutSyncForce       bool
	rolloutConfirm         bool
	rolloutReason          string
	rolloutFor             time.Duration
)

func init() {
	rootCmd.AddCommand(rolloutCmd)

	rolloutCmd.AddCommand(
		rolloutNetworksCmd, rolloutStatusCmd, rolloutListCmd, rolloutShowCmd, rolloutHistoryCmd,
		rolloutSyncCmd, rolloutRefreshCmd, rolloutVerbCmd("pause", "Stop a rollout after its current batch"),
		rolloutVerbCmd("promote", "Continue a paused rollout"), rolloutVerbCmd("abort", "Close a rollout; the group stays out of sync"),
		rolloutRetryCmd, rolloutSuspendCmd, rolloutResumeCmd,
	)

	for _, c := range rolloutCmd.Commands() {
		if c != rolloutNetworksCmd {
			c.ValidArgsFunction = completeOperationNetworkNames("rolloor.list_networks")
		}
	}

	rolloutHistoryCmd.Flags().StringVar(&rolloutHistoryGroup, "group", "", "only this group (client)")
	rolloutHistoryCmd.Flags().StringVar(&rolloutHistorySelector, "selector", "", "only events about targets this selector matches, e.g. node=lighthouse-geth-1")
	rolloutHistoryCmd.Flags().IntVar(&rolloutHistoryLimit, "limit", 50, "how many events")

	rolloutSyncCmd.Flags().StringVar(&rolloutSyncStrategy, "strategy", "", "a named strategy for the rollout this starts")
	rolloutSyncCmd.Flags().BoolVar(&rolloutSyncForce, "force", false, "skip readiness and soak (never the disruption budget)")

	for _, c := range []*cobra.Command{rolloutSyncCmd, rolloutSuspendCmd, rolloutResumeCmd} {
		c.Flags().BoolVar(&rolloutConfirm, "confirm", false, "confirm acting across more than one owner")
	}

	rolloutRetryCmd.Flags().StringVar(&rolloutReason, "reason", "", "why the batch may pass this time (required)")
	rolloutSuspendCmd.Flags().StringVar(&rolloutReason, "reason", "", "why (required)")
	rolloutSuspendCmd.Flags().DurationVar(&rolloutFor, "for", 24*time.Hour, "how long the suspension lasts")
}

var rolloutNetworksCmd = &cobra.Command{
	Use:   "networks",
	Short: "List devnets that run rolloor",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		response, err := runServerOperation(cmd, "rolloor.list_networks", map[string]any{})
		if err != nil {
			return err
		}

		return printListing(response, "networks", "No devnet runs rolloor yet.")
	},
}

var rolloutStatusCmd = &cobra.Command{
	Use:   "status <network>",
	Short: "Every group's sync, health and rollout, and the disruption budget",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		response, err := runServerOperation(cmd, "rolloor.fleet", map[string]any{"network": args[0]})
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSON(response)
		}

		fleet, _ := response.Data.(map[string]any)
		groups, _ := fleet["groups"].([]any)

		rows := make([][]string, 0, len(groups))

		for _, raw := range groups {
			g, _ := raw.(map[string]any)
			state := "-"

			if r, ok := g["rollout"].(map[string]any); ok {
				state = str(r["state"])
			}

			rows = append(rows, []string{
				str(g["name"]), fmt.Sprintf("%s/%s", str(g["onDesired"]), str(g["targets"])), str(g["sync"]), str(g["health"]), state, str(g["reason"]),
			})
		}

		printKeyValue([][2]string{
			{"Network", args[0]},
			{"Disruption budget", fmt.Sprintf("%s of %s unavailable", str(fleet["unavailable"]), str(fleet["maxUnavailable"]))},
		})
		fmt.Println()
		printTable([]string{"GROUP", "ON BUILD", "SYNC", "HEALTH", "ROLLOUT", "REASON"}, rows)

		return nil
	},
}

var rolloutListCmd = &cobra.Command{
	Use:   "list <network>",
	Short: "Rollouts, newest first",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		response, err := runServerOperation(cmd, "rolloor.rollouts", map[string]any{"network": args[0]})
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSON(response)
		}

		list, _ := response.Data.([]any)
		if len(list) == 0 {
			fmt.Println("No rollouts yet.")

			return nil
		}

		rows := make([][]string, 0, len(list))

		for _, raw := range list {
			r, _ := raw.(map[string]any)
			rows = append(rows, []string{
				str(r["id"]), str(r["group"]), str(r["state"]), str(r["digest"]),
				fmt.Sprintf("%s/%s", str(r["onNewBuild"]), str(r["total"])), shortTime(str(r["createdAt"])),
			})
		}

		printTable([]string{"ID", "GROUP", "STATE", "BUILD", "ON BUILD", "STARTED"}, rows)

		return nil
	},
}

var rolloutShowCmd = &cobra.Command{
	Use:   "show <network> <rollout-id>",
	Short: "One rollout: its state, reason and batches",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		response, err := runServerOperation(cmd, "rolloor.rollout", map[string]any{"network": args[0], "id": args[1]})
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSON(response)
		}

		r, _ := response.Data.(map[string]any)
		strategy := str(r["strategy"])

		if strategy == "" || strategy == "-" {
			strategy = "default"
		}

		printKeyValue([][2]string{
			{"Rollout", str(r["id"])},
			{"Group", str(r["group"])},
			{"State", str(r["state"])},
			{"Reason", str(r["reason"])},
			{"Build", strings.TrimSpace(str(r["digest"]) + " " + optional(r["revision"]))},
			{"Strategy", strategy},
			{"On build", fmt.Sprintf("%s of %s", str(r["onNewBuild"]), str(r["total"]))},
			{"Disruption budget", str(r["unavailable"])},
		})

		batches, _ := r["batches"].([]any)
		if len(batches) == 0 {
			return nil
		}

		rows := make([][]string, 0, len(batches))

		for _, raw := range batches {
			b, _ := raw.(map[string]any)
			targets, _ := b["targets"].([]any)

			names := make([]string, 0, len(targets))
			for _, t := range targets {
				names = append(names, str(t))
			}

			status := "open"
			if str(b["endedAt"]) != "-" {
				status = "halted"
				if b["passed"] == true {
					status = "passed"
				}
			}

			rows = append(rows, []string{str(b["number"]), str(b["wave"]), status, strings.Join(names, ", ")})
		}

		fmt.Println()
		printTable([]string{"BATCH", "WAVE", "STATUS", "TARGETS"}, rows)

		return nil
	},
}

var rolloutHistoryCmd = &cobra.Command{
	Use:   "history <network>",
	Short: "What happened, newest first",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		response, err := runServerOperation(cmd, "rolloor.history", map[string]any{
			"network": args[0], "group": rolloutHistoryGroup, "selector": rolloutHistorySelector, "limit": rolloutHistoryLimit,
		})
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSON(response)
		}

		events, _ := response.Data.([]any)
		if len(events) == 0 {
			fmt.Println("No history.")

			return nil
		}

		rows := make([][]string, 0, len(events))

		for _, raw := range events {
			e, _ := raw.(map[string]any)
			rows = append(rows, []string{shortTime(str(e["at"])), str(e["actor"]), str(e["action"]), str(e["group"]), str(e["reason"])})
		}

		printTable([]string{"TIME", "ACTOR", "ACTION", "GROUP", "REASON"}, rows)

		return nil
	},
}

var rolloutSyncCmd = &cobra.Command{
	Use:   "sync <network> <selector>",
	Short: "Start or hurry the rollout of every group the selector touches",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return rolloutAct(cmd, args[0], "sync", map[string]any{
			"selector": args[1], "strategy": rolloutSyncStrategy, "force": rolloutSyncForce, "confirm": rolloutConfirm,
		})
	},
}

var rolloutRefreshCmd = &cobra.Command{
	Use:   "refresh <network>",
	Short: "Resolve every image tag now",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return rolloutAct(cmd, args[0], "refresh", map[string]any{})
	},
}

// rolloutVerbCmd builds the commands that take only a rollout id.
func rolloutVerbCmd(verb, short string) *cobra.Command {
	return &cobra.Command{
		Use:   verb + " <network> <rollout-id>",
		Short: short,
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return rolloutAct(cmd, args[0], verb, map[string]any{"rollout": args[1]})
		},
	}
}

var rolloutRetryCmd = &cobra.Command{
	Use:   "retry <network> <rollout-id>",
	Short: "Reopen a halted rollout's batch",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if rolloutReason == "" {
			return fmt.Errorf("--reason is required")
		}

		return rolloutAct(cmd, args[0], "retry", map[string]any{"rollout": args[1], "reason": rolloutReason})
	},
}

var rolloutSuspendCmd = &cobra.Command{
	Use:   "suspend <network> <selector>",
	Short: "Keep the selected targets out of every rollout for a while",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if rolloutReason == "" {
			return fmt.Errorf("--reason is required")
		}

		return rolloutAct(cmd, args[0], "suspend", map[string]any{
			"selector": args[1], "reason": rolloutReason, "expiresIn": rolloutFor.String(), "confirm": rolloutConfirm,
		})
	},
}

var rolloutResumeCmd = &cobra.Command{
	Use:   "resume <network> <selector>",
	Short: "Lift the suspensions with exactly this selector",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return rolloutAct(cmd, args[0], "resume", map[string]any{"selector": args[1], "confirm": rolloutConfirm})
	},
}

// rolloutAct sends one verb and prints what rolloor answered.
func rolloutAct(cmd *cobra.Command, network, verb string, body map[string]any) error {
	response, err := runServerOperation(cmd, "rolloor.action", map[string]any{"network": network, "verb": verb, "body": body})
	if err != nil {
		return err
	}

	if isJSON() {
		return printJSON(response)
	}

	data, _ := response.Data.(map[string]any)

	switch {
	case data["rollouts"] != nil:
		ids, _ := data["rollouts"].([]any)

		names := make([]string, 0, len(ids))
		for _, id := range ids {
			names = append(names, str(id))
		}

		fmt.Printf("%s: rollouts %s\n", verb, strings.Join(names, ", "))
	case data["lifted"] != nil:
		fmt.Printf("%s: %s suspensions lifted\n", verb, str(data["lifted"]))
	case data["state"] != nil:
		fmt.Printf("%s: %s is %s\n", verb, str(data["id"]), str(data["state"]))
	case data["id"] != nil:
		fmt.Printf("%s: %s\n", verb, str(data["id"]))
	default:
		fmt.Printf("%s: done\n", verb)
	}

	return nil
}

// str renders a decoded JSON value for a table cell.
func str(v any) string {
	switch x := v.(type) {
	case nil:
		return "-"
	case string:
		if x == "" {
			return "-"
		}

		return x
	case float64:
		return fmt.Sprintf("%g", x)
	default:
		return fmt.Sprint(x)
	}
}

func optional(v any) string {
	if s, ok := v.(string); ok {
		return "(" + s + ")"
	}

	return ""
}

// shortTime turns an RFC 3339 time into a compact UTC stamp.
func shortTime(s string) string {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return s
	}

	return t.UTC().Format("2006-01-02 15:04:05")
}
