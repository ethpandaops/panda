package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
)

var benchmarkoorCmd = &cobra.Command{
	GroupID: groupDirect,
	Use:     "benchmarkoor",
	Short:   "Query execution-client benchmark results: runs, suites, throughput",
	Long: `Query benchmarkoor, the execution-client benchmarking service. Runs replay
standardized test fixtures against EL clients (geth, reth, nethermind, besu,
erigon, ...) and record gas throughput (MGas/s), per-test wall time, system
resources, and per-block EL timing internals.

Runs of the same test set share a suite hash; use 'suite-stats' to compare
clients on identical workloads. Query commands accept PostgREST-style
--filter flags (column=operator.value) and --order (column.asc|desc).

The --datasource flag can be omitted when a single benchmarkoor datasource is
configured.

Examples:
  panda benchmarkoor datasources
  panda benchmarkoor runs --client geth --limit 10
  panda benchmarkoor runs --filter tests_failed=gt.0
  panda benchmarkoor run <run_id>
  panda benchmarkoor suites
  panda benchmarkoor suite-stats <suite_hash>
  panda benchmarkoor tests --run <run_id> --order test_mgas_s.desc
  panda benchmarkoor block-logs --run <run_id> --order timing_total_ms.desc
  panda benchmarkoor live
  panda benchmarkoor file <discovery_path>/runs/<run_id>/result.json
  panda benchmarkoor link <run_id>`,
}

var (
	benchmarkoorDatasource  string
	benchmarkoorClient      string
	benchmarkoorStatus      string
	benchmarkoorSuite       string
	benchmarkoorRunID       string
	benchmarkoorTestName    string
	benchmarkoorFilters     []string
	benchmarkoorOrder       string
	benchmarkoorSelect      string
	benchmarkoorLimit       int
	benchmarkoorOffset      int
	benchmarkoorMaxRuns     int
	benchmarkoorLinkIsSuite bool
)

func init() {
	rootCmd.AddCommand(benchmarkoorCmd)

	benchmarkoorCmd.AddCommand(
		benchmarkoorDatasourcesCmd,
		benchmarkoorRunsCmd,
		benchmarkoorRunCmd,
		benchmarkoorSuitesCmd,
		benchmarkoorSuiteStatsCmd,
		benchmarkoorTestsCmd,
		benchmarkoorBlockLogsCmd,
		benchmarkoorLiveCmd,
		benchmarkoorFileCmd,
		benchmarkoorLinkCmd,
	)

	for _, cmd := range []*cobra.Command{
		benchmarkoorRunsCmd,
		benchmarkoorRunCmd,
		benchmarkoorSuitesCmd,
		benchmarkoorSuiteStatsCmd,
		benchmarkoorTestsCmd,
		benchmarkoorBlockLogsCmd,
		benchmarkoorLiveCmd,
		benchmarkoorFileCmd,
		benchmarkoorLinkCmd,
	} {
		cmd.Flags().StringVar(&benchmarkoorDatasource, "datasource", "",
			"Benchmarkoor datasource name (optional when only one is configured)")
		_ = cmd.RegisterFlagCompletionFunc("datasource", func(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			return completeDatasourceNames("benchmarkoor")(cmd, nil, toComplete)
		})
	}

	for _, cmd := range []*cobra.Command{
		benchmarkoorRunsCmd,
		benchmarkoorSuitesCmd,
		benchmarkoorTestsCmd,
		benchmarkoorBlockLogsCmd,
	} {
		cmd.Flags().StringArrayVar(&benchmarkoorFilters, "filter", nil,
			"PostgREST filter, column=operator.value (e.g. tests_failed=gt.0); repeatable")
		cmd.Flags().StringVar(&benchmarkoorOrder, "order", "", "Sort directives (e.g. timestamp.desc)")
		cmd.Flags().IntVar(&benchmarkoorLimit, "limit", 0, "Maximum rows to return")
		cmd.Flags().IntVar(&benchmarkoorOffset, "offset", 0, "Rows to skip")
	}

	benchmarkoorRunsCmd.Flags().StringVar(&benchmarkoorClient, "client", "", "Filter by execution client")
	benchmarkoorRunsCmd.Flags().StringVar(&benchmarkoorStatus, "status", "",
		"Filter by status (running, completed, failed, container_died, cancelled, timeout)")
	benchmarkoorRunsCmd.Flags().StringVar(&benchmarkoorSuite, "suite", "", "Filter by suite hash")

	benchmarkoorSuiteStatsCmd.Flags().IntVar(&benchmarkoorMaxRuns, "max-runs-per-client", 0,
		"Cap recent runs per client included in the stats")

	for _, cmd := range []*cobra.Command{benchmarkoorTestsCmd, benchmarkoorBlockLogsCmd} {
		cmd.Flags().StringVar(&benchmarkoorRunID, "run", "", "Filter by run ID")
		cmd.Flags().StringVar(&benchmarkoorClient, "client", "", "Filter by execution client")
		cmd.Flags().StringVar(&benchmarkoorTestName, "test", "", "Filter by test name")
		cmd.Flags().StringVar(&benchmarkoorSelect, "select", "", "Comma-separated columns to return")
	}

	benchmarkoorTestsCmd.Flags().StringVar(&benchmarkoorSuite, "suite", "", "Filter by suite hash")

	benchmarkoorLinkCmd.Flags().BoolVar(&benchmarkoorLinkIsSuite, "suite", false,
		"Treat the ID as a suite hash and link to the suite page")
}

var benchmarkoorDatasourcesCmd = &cobra.Command{
	Use:   "datasources",
	Short: "List benchmarkoor datasources",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		response, err := runServerOperation(cmd, "benchmarkoor.list_datasources", map[string]any{})
		if err != nil {
			return err
		}

		return printListing(response, "datasources", "No benchmarkoor datasources found.")
	},
}

var benchmarkoorRunsCmd = &cobra.Command{
	Use:   "runs",
	Short: "List indexed benchmark runs",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		opArgs, err := benchmarkoorQueryArgs()
		if err != nil {
			return err
		}

		setIfNotEmpty(opArgs, "client", benchmarkoorClient)
		setIfNotEmpty(opArgs, "status", benchmarkoorStatus)
		setIfNotEmpty(opArgs, "suite_hash", benchmarkoorSuite)

		rows, err := runBenchmarkoorQuery(cmd, "benchmarkoor.list_runs", opArgs)
		if err != nil || rows == nil {
			return err
		}

		if len(rows) == 0 {
			fmt.Println("No benchmark runs found.")
			return nil
		}

		for _, item := range rows {
			run, _ := item.(map[string]any)
			fmt.Printf("  %v  %s  %-12v %-10v passed=%v/%v\n",
				run["run_id"],
				benchmarkoorTimestamp(run["timestamp"]),
				run["client"],
				run["status"],
				formatJSONNumber(run["tests_passed"]),
				formatJSONNumber(run["tests_total"]),
			)
		}

		return nil
	},
}

var benchmarkoorRunCmd = &cobra.Command{
	Use:   "run <run_id>",
	Short: "Get one benchmark run with per-step stats (always JSON)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		opArgs := map[string]any{"run_id": args[0]}
		setIfNotEmpty(opArgs, "datasource", benchmarkoorDatasource)

		response, err := runServerOperationRaw(cmd, "benchmarkoor.get_run", opArgs)
		if err != nil {
			return err
		}

		return printJSONBytes(response.Body)
	},
}

var benchmarkoorSuitesCmd = &cobra.Command{
	Use:   "suites",
	Short: "List indexed test suites",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		opArgs, err := benchmarkoorQueryArgs()
		if err != nil {
			return err
		}

		rows, err := runBenchmarkoorQuery(cmd, "benchmarkoor.list_suites", opArgs)
		if err != nil || rows == nil {
			return err
		}

		if len(rows) == 0 {
			fmt.Println("No suites found.")
			return nil
		}

		for _, item := range rows {
			suite, _ := item.(map[string]any)
			fmt.Printf("  %v  %v  tests=%v\n",
				suite["suite_hash"], suite["name"], formatJSONNumber(suite["tests_total"]))
		}

		return nil
	},
}

var benchmarkoorSuiteStatsCmd = &cobra.Command{
	Use:   "suite-stats <suite_hash>",
	Short: "Per-test duration/gas history for a suite across clients (always JSON)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		opArgs := map[string]any{"suite_hash": args[0]}
		setIfNotEmpty(opArgs, "datasource", benchmarkoorDatasource)

		if benchmarkoorMaxRuns > 0 {
			opArgs["max_runs_per_client"] = benchmarkoorMaxRuns
		}

		response, err := runServerOperationRaw(cmd, "benchmarkoor.get_suite_stats", opArgs)
		if err != nil {
			return err
		}

		return printJSONBytes(response.Body)
	},
}

var benchmarkoorTestsCmd = &cobra.Command{
	Use:   "tests",
	Short: "Query per-test stats: gas used, wall time, MGas/s, resources",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		opArgs, err := benchmarkoorQueryArgs()
		if err != nil {
			return err
		}

		setIfNotEmpty(opArgs, "run_id", benchmarkoorRunID)
		setIfNotEmpty(opArgs, "client", benchmarkoorClient)
		setIfNotEmpty(opArgs, "test_name", benchmarkoorTestName)
		setIfNotEmpty(opArgs, "suite_hash", benchmarkoorSuite)
		setIfNotEmpty(opArgs, "select", benchmarkoorSelect)

		rows, err := runBenchmarkoorQuery(cmd, "benchmarkoor.query_test_stats", opArgs)
		if err != nil || rows == nil {
			return err
		}

		if len(rows) == 0 {
			fmt.Println("No test stats found.")
			return nil
		}

		for _, item := range rows {
			stat, _ := item.(map[string]any)
			fmt.Printf("  %v  %-12v %v  %v MGas/s\n",
				stat["test_name"], stat["client"], stat["run_id"],
				formatJSONNumber(stat["test_mgas_s"]))
		}

		return nil
	},
}

var benchmarkoorBlockLogsCmd = &cobra.Command{
	Use:   "block-logs",
	Short: "Query per-block EL timing: execution/state/commit ms, throughput",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		opArgs, err := benchmarkoorQueryArgs()
		if err != nil {
			return err
		}

		setIfNotEmpty(opArgs, "run_id", benchmarkoorRunID)
		setIfNotEmpty(opArgs, "client", benchmarkoorClient)
		setIfNotEmpty(opArgs, "test_name", benchmarkoorTestName)
		setIfNotEmpty(opArgs, "select", benchmarkoorSelect)

		rows, err := runBenchmarkoorQuery(cmd, "benchmarkoor.query_block_logs", opArgs)
		if err != nil || rows == nil {
			return err
		}

		if len(rows) == 0 {
			fmt.Println("No block logs found.")
			return nil
		}

		for _, item := range rows {
			row, _ := item.(map[string]any)
			fmt.Printf("  block=%v  %-12v total=%vms exec=%vms  %v MGas/s\n",
				formatJSONNumber(row["block_number"]),
				row["client"],
				formatJSONNumber(row["timing_total_ms"]),
				formatJSONNumber(row["timing_execution_ms"]),
				formatJSONNumber(row["throughput_mgas_per_sec"]),
			)
		}

		return nil
	},
}

var benchmarkoorLiveCmd = &cobra.Command{
	Use:   "live",
	Short: "List currently-running benchmark runs",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		opArgs := map[string]any{}
		setIfNotEmpty(opArgs, "datasource", benchmarkoorDatasource)

		response, err := runServerOperationRaw(cmd, "benchmarkoor.list_live_runs", opArgs)
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSONBytes(response.Body)
		}

		var rows []map[string]any
		if err := json.Unmarshal(response.Body, &rows); err != nil {
			return fmt.Errorf("decoding benchmarkoor response: %w", err)
		}

		if len(rows) == 0 {
			fmt.Println("No benchmark runs currently executing.")
			return nil
		}

		for _, run := range rows {
			fmt.Printf("  %v  %-12v %v/%v passed  last_report=%v\n",
				run["run_id"], run["client"],
				formatJSONNumber(run["tests_passed"]),
				formatJSONNumber(run["tests_total"]),
				run["last_reported_at"],
			)
		}

		return nil
	},
}

var benchmarkoorFileCmd = &cobra.Command{
	Use:   "file <path>",
	Short: "Fetch a stored result file (e.g. <discovery_path>/runs/<run_id>/result.json)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		opArgs := map[string]any{"path": args[0]}
		setIfNotEmpty(opArgs, "datasource", benchmarkoorDatasource)

		response, err := runServerOperationRaw(cmd, "benchmarkoor.get_file", opArgs)
		if err != nil {
			return err
		}

		_, err = os.Stdout.Write(response.Body)

		return err
	},
}

var benchmarkoorLinkCmd = &cobra.Command{
	Use:   "link <run_id | suite_hash>",
	Short: "Print a deep link to a run (or, with --suite, a suite) in the web UI",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		operationID := "benchmarkoor.link_run"
		opArgs := map[string]any{"run_id": args[0]}

		if benchmarkoorLinkIsSuite {
			operationID = "benchmarkoor.link_suite"
			opArgs = map[string]any{"suite_hash": args[0]}
		}

		setIfNotEmpty(opArgs, "datasource", benchmarkoorDatasource)

		response, err := runServerOperation(cmd, operationID, opArgs)
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSON(response)
		}

		data, _ := response.Data.(map[string]any)
		fmt.Printf("%v\n", data["url"])

		return nil
	},
}

// benchmarkoorQueryArgs assembles the shared query args: datasource,
// pagination, ordering, and --filter flags.
func benchmarkoorQueryArgs() (map[string]any, error) {
	opArgs := map[string]any{}
	setIfNotEmpty(opArgs, "datasource", benchmarkoorDatasource)
	setIfNotEmpty(opArgs, "order", benchmarkoorOrder)

	if benchmarkoorLimit > 0 {
		opArgs["limit"] = benchmarkoorLimit
	}

	if benchmarkoorOffset > 0 {
		opArgs["offset"] = benchmarkoorOffset
	}

	if len(benchmarkoorFilters) > 0 {
		filters := make(map[string]any, len(benchmarkoorFilters))

		for _, raw := range benchmarkoorFilters {
			column, value, ok := cutFilterFlag(raw)
			if !ok {
				return nil, fmt.Errorf("invalid --filter %q: expected column=operator.value (e.g. tests_failed=gt.0)", raw)
			}

			filters[column] = value
		}

		opArgs["filters"] = filters
	}

	return opArgs, nil
}

// runBenchmarkoorQuery runs a PostgREST-style query operation and returns the
// rows for human rendering. In JSON output mode it prints the raw body and
// returns (nil, nil) so callers skip rendering.
func runBenchmarkoorQuery(cmd *cobra.Command, operationID string, args map[string]any) ([]any, error) {
	response, err := runServerOperationRaw(cmd, operationID, args)
	if err != nil {
		return nil, err
	}

	if isJSON() {
		return nil, printJSONBytes(response.Body)
	}

	var body map[string]any
	if err := json.Unmarshal(response.Body, &body); err != nil {
		return nil, fmt.Errorf("decoding benchmarkoor response: %w", err)
	}

	rows, _ := body["data"].([]any)

	return rows, nil
}

func cutFilterFlag(raw string) (string, string, bool) {
	for i := 0; i < len(raw); i++ {
		if raw[i] == '=' {
			if i == 0 || i == len(raw)-1 {
				return "", "", false
			}

			return raw[:i], raw[i+1:], true
		}
	}

	return "", "", false
}

// benchmarkoorTimestamp renders a unix-seconds timestamp as UTC RFC3339.
func benchmarkoorTimestamp(value any) string {
	number, ok := value.(float64)
	if !ok || number <= 0 {
		return "-"
	}

	return time.Unix(int64(number), 0).UTC().Format(time.RFC3339)
}
