package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

var hiveCmd = &cobra.Command{
	GroupID: groupDirect,
	Use:     "hive",
	Short:   "Query hive test results",
	Long: `Query the hive test results ethPandaOps CI publishes at hive.ethpandaops.io:
which EELS tests each execution client passes and fails, with errors, logs,
reproduce commands and a link to each test's page.

Examples:
  panda hive groups
  panda hive summary glamsterdam-quick
  panda hive failures glamsterdam-quick --client reth
  panda hive tests glamsterdam-quick --client reth --filter eip7928
  panda hive find glamsterdam-quick test_bal_ --client reth
  panda hive diff glamsterdam-quick`,
}

// hiveFlags are the filters the test commands share.
type hiveFlags struct {
	client    string
	simulator string
	status    string
	filter    string
	limit     int
	fullLog   bool
}

var hiveOpts hiveFlags

func init() {
	rootCmd.AddCommand(hiveCmd)

	hiveCmd.AddCommand(
		hiveGroupsCmd,
		hiveSummaryCmd,
		hiveFailuresCmd,
		hiveTestsCmd,
		hiveSearchCmd,
		hiveTestCmd,
		hiveFindCmd,
		hiveDiffCmd,
	)

	for _, cmd := range []*cobra.Command{hiveFailuresCmd, hiveTestsCmd, hiveSearchCmd, hiveFindCmd} {
		cmd.Flags().StringVar(&hiveOpts.client, "client", "", "Filter by execution client (e.g. reth, go-ethereum)")
		cmd.Flags().StringVar(&hiveOpts.simulator, "simulator", "", "Filter by simulator (e.g. consume-engine)")
	}

	for _, cmd := range []*cobra.Command{hiveTestsCmd, hiveSearchCmd} {
		cmd.Flags().IntVar(&hiveOpts.limit, "limit", 50, "Max tests to list, 0 for all")
	}

	hiveTestsCmd.Flags().StringVar(&hiveOpts.status, "status", "fail", "fail, pass or all")
	hiveTestsCmd.Flags().StringVar(&hiveOpts.filter, "filter", "", "Case-insensitive regexp on the pytest ID")
	hiveSearchCmd.Flags().StringVar(&hiveOpts.status, "status", "all", "fail, pass or all")

	for _, cmd := range []*cobra.Command{hiveTestCmd, hiveFindCmd} {
		cmd.Flags().BoolVar(&hiveOpts.fullLog, "full-log", false, "Show the whole client log, not its last 30 lines")
	}
}

var hiveGroupsCmd = &cobra.Command{
	Use:   "groups",
	Short: "List result groups",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		response, err := runServerOperation(cmd, "hive.list_groups", map[string]any{})
		if err != nil {
			return err
		}

		return printListing(response, "groups", "No hive groups found.")
	},
}

var hiveSummaryCmd = &cobra.Command{
	Use:   "summary <group>",
	Short: "Pass and fail counts per client",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		response, err := runServerOperation(cmd, "hive.get_summary", map[string]any{"group": args[0]})
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSON(response)
		}

		data, _ := response.Data.(map[string]any)
		fmt.Printf("%s, last run %v\n\n", args[0], data["last_run"])

		clients, _ := data["clients"].(map[string]any)
		rows := make([][]string, 0, len(clients))

		for _, name := range sortedKeys(clients) {
			c, _ := clients[name].(map[string]any)
			rows = append(rows, []string{
				name, str(c["version"]), str(c["pass"]), str(c["fail"]), str(c["timeout"]), str(c["branch"]),
			})
		}

		printTable([]string{"CLIENT", "VERSION", "PASS", "FAIL", "TIMEOUT", "BRANCH"}, rows)

		return nil
	},
}

var hiveFailuresCmd = &cobra.Command{
	Use:   "failures <group>",
	Short: "Failures and fail rates per client",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		response, err := runServerOperation(cmd, "hive.get_failures", map[string]any{
			"group":     args[0],
			"client":    hiveOpts.client,
			"simulator": hiveOpts.simulator,
		})
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSON(response)
		}

		data, _ := response.Data.(map[string]any)
		clients, _ := data["clients"].(map[string]any)
		rows := [][]string{}

		for _, name := range sortedKeys(clients) {
			c, _ := clients[name].(map[string]any)
			sims, _ := c["simulators"].(map[string]any)

			for _, sim := range sortedKeys(sims) {
				s, _ := sims[sim].(map[string]any)
				rate, _ := s["fail_rate"].(float64)
				rows = append(rows, []string{
					name, sim, str(s["total_fails"]), str(s["total_tests"]), fmt.Sprintf("%.1f%%", rate), str(c["branch"]),
				})
			}
		}

		printTable([]string{"CLIENT", "SIMULATOR", "FAILS", "TESTS", "RATE", "BRANCH"}, rows)

		return nil
	},
}

var hiveTestsCmd = &cobra.Command{
	Use:   "tests <group>",
	Short: "List failing (or passing) tests, with links",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHiveTestList(cmd, "hive.list_tests", map[string]any{
			"group":     args[0],
			"client":    hiveOpts.client,
			"simulator": hiveOpts.simulator,
			"status":    hiveOpts.status,
			"filter":    hiveOpts.filter,
			"limit":     hiveOpts.limit,
		})
	},
}

var hiveSearchCmd = &cobra.Command{
	Use:   "search <group> <query>",
	Short: "Tests whose name contains a query",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHiveTestList(cmd, "hive.search", map[string]any{
			"group":     args[0],
			"query":     args[1],
			"client":    hiveOpts.client,
			"simulator": hiveOpts.simulator,
			"status":    hiveOpts.status,
			"limit":     hiveOpts.limit,
		})
	},
}

func runHiveTestList(cmd *cobra.Command, operation string, args map[string]any) error {
	response, err := runServerOperation(cmd, operation, args)
	if err != nil {
		return err
	}

	if isJSON() {
		return printJSON(response)
	}

	data, _ := response.Data.(map[string]any)
	tests, _ := data["tests"].([]any)
	fmt.Printf("%v tests, showing %d\n\n", data["total"], len(tests))

	for _, item := range tests {
		t, _ := item.(map[string]any)
		status := "FAIL"

		if pass, _ := t["pass"].(bool); pass {
			status = "PASS"
		}

		fmt.Printf("[%s] %s  %s  %s\n", status, t["client"], t["simulator"], t["pytest_id"])
		fmt.Printf("       %s\n", t["hive_url"])
	}

	return nil
}

var hiveTestCmd = &cobra.Command{
	Use:   "test <group> <suite-file> <test-id>",
	Short: "One test's error, logs and reproduce commands",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHiveDetail(cmd, "hive.get_test", map[string]any{
			"group":      args[0],
			"suite_file": args[1],
			"test_id":    args[2],
			"full_log":   hiveOpts.fullLog,
		})
	},
}

var hiveFindCmd = &cobra.Command{
	Use:   "find <group> <filter>",
	Short: "Detail of the first test matching a regexp",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHiveDetail(cmd, "hive.find_test", map[string]any{
			"group":     args[0],
			"filter":    args[1],
			"client":    hiveOpts.client,
			"simulator": hiveOpts.simulator,
			"full_log":  hiveOpts.fullLog,
		})
	},
}

func runHiveDetail(cmd *cobra.Command, operation string, args map[string]any) error {
	response, err := runServerOperation(cmd, operation, args)
	if err != nil {
		return err
	}

	if isJSON() {
		return printJSON(response)
	}

	d, _ := response.Data.(map[string]any)
	pairs := [][2]string{
		{"Test", str(d["pytest_id"])},
		{"Client", str(d["client"])},
		{"Pass", str(d["pass"])},
		{"Hive", str(d["hive_url"])},
		{"Source", str(d["source_url"])},
		{"Fill", str(d["fill_command"])},
		{"Consume", str(d["consume_command"])},
		{"Hive command", str(d["hive_command"])},
	}
	printKeyValue(pairs)

	for _, section := range [][2]string{{"Error", "error"}, {"Detail log", "detail_log"}, {"Client log", "client_log"}} {
		if text := strings.TrimSpace(str(d[section[1]])); text != "" {
			fmt.Printf("\n%s\n%s\n%s\n", section[0], strings.Repeat("-", len(section[0])), text)
		}
	}

	return nil
}

var hiveDiffCmd = &cobra.Command{
	Use:   "diff <group>",
	Short: "New failures and fixes since the previous run",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		response, err := runServerOperation(cmd, "hive.get_diff", map[string]any{"group": args[0]})
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSON(response)
		}

		data, _ := response.Data.(map[string]any)
		fmt.Printf("%s, from %v to %v\n\n", args[0], data["from_run"], data["to_run"])

		for _, section := range [][2]string{
			{"New failures", "regressions"},
			{"Fixed", "fixes"},
			{"Still failing", "unchanged_fails"},
		} {
			clients, _ := data[section[1]].(map[string]any)
			parts := make([]string, 0, len(clients))

			for _, name := range sortedKeys(clients) {
				c, _ := clients[name].(map[string]any)
				parts = append(parts, fmt.Sprintf("%s %v", name, c["total"]))
			}

			fmt.Printf("%-14s %s\n", section[0]+":", strings.Join(parts, ", "))
		}

		return nil
	},
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return keys
}

// str formats a JSON-decoded value, with numbers as integers when whole.
func str(v any) string {
	switch value := v.(type) {
	case nil:
		return ""
	case float64:
		if value == float64(int64(value)) {
			return fmt.Sprintf("%d", int64(value))
		}

		return fmt.Sprintf("%g", value)
	default:
		return fmt.Sprintf("%v", value)
	}
}
