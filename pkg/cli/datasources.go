package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

var datasourcesType string

var datasourcesCmd = &cobra.Command{
	GroupID: groupDiscovery,
	Use:     "datasources",
	Short:   "List available datasources from the server",
	Long: `List all datasources exposed by the configured server, including
ClickHouse, Prometheus, Loki, Ethnode, and other discovered types.

Examples:
  panda datasources                     # List all datasources
  panda datasources --type clickhouse   # List only ClickHouse datasources
  panda datasources --json              # Output as JSON`,
	RunE: runDatasources,
}

func init() {
	rootCmd.AddCommand(datasourcesCmd)
	datasourcesCmd.Flags().StringVar(&datasourcesType, "type", "", "Filter by type (clickhouse, prometheus, loki, ethnode)")

	_ = datasourcesCmd.RegisterFlagCompletionFunc("type", cobra.FixedCompletions(
		[]string{"clickhouse", "prometheus", "loki", "ethnode"}, cobra.ShellCompDirectiveNoFileComp,
	))
}

func runDatasources(cmd *cobra.Command, _ []string) error {
	response, err := listDatasources(cmd.Context(), datasourcesType)
	if err != nil {
		return fmt.Errorf("listing datasources: %w", err)
	}

	if isJSON() {
		return printJSON(response)
	}

	if len(response.Datasources) == 0 {
		fmt.Println("No datasources found.")

		return nil
	}

	rows := make([][]string, 0, len(response.Datasources))
	hasClickHouse := false

	for _, info := range response.Datasources {
		if info.Type == "clickhouse" {
			hasClickHouse = true
		}

		desc := info.Description
		if desc == "" {
			desc = info.Name
		}

		// Dataset names only, deduplicated: a dataset bound more than once
		// (e.g. otel-logs in two databases) is one entry here. Placement
		// detail lives in `panda datasets`.
		seen := make(map[string]bool, len(info.Contents))
		datasets := make([]string, 0, len(info.Contents))

		for _, b := range info.Contents {
			if seen[b.Dataset] {
				continue
			}

			seen[b.Dataset] = true

			datasets = append(datasets, b.Dataset)
		}

		rows = append(rows, []string{info.Type, info.Name, strings.Join(datasets, ", "), desc})
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i][0] != rows[j][0] {
			return rows[i][0] < rows[j][0]
		}

		return rows[i][1] < rows[j][1]
	})

	printTable([]string{"TYPE", "NAME", "DATASETS", "DESCRIPTION"}, rows)

	fmt.Println("\nDataset placements and notes: panda datasets")

	if hasClickHouse {
		fmt.Println("ClickHouse schemas: panda schema <cluster> [database] [table]")
	}

	return nil
}

// formatBindingParams renders a binding's opaque params as sorted "k=v" pairs.
func formatBindingParams(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, k+"="+params[k])
	}

	return strings.Join(pairs, ", ")
}
