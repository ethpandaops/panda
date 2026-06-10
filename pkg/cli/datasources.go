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

	type noteLine struct{ source, dataset, note string }

	var notes []noteLine

	for _, info := range response.Datasources {
		if info.Type == "clickhouse" {
			hasClickHouse = true
		}

		desc := info.Description
		if desc == "" {
			desc = info.Name
		}

		// The same dataset may be bound more than once to one datasource (e.g.
		// otel-logs in both the internal and external databases); annotate
		// duplicates with their params so the bindings stay distinguishable.
		counts := make(map[string]int, len(info.Contents))
		for _, b := range info.Contents {
			counts[b.Dataset]++
		}

		datasets := make([]string, 0, len(info.Contents))

		for _, b := range info.Contents {
			label := b.Dataset
			if counts[b.Dataset] > 1 && len(b.Params) > 0 {
				label += " (" + formatBindingParams(b.Params) + ")"
			}

			datasets = append(datasets, label)

			if b.Notes != "" {
				key := b.Dataset
				if len(b.Params) > 0 {
					key += " (" + formatBindingParams(b.Params) + ")"
				}

				notes = append(notes, noteLine{source: info.Name, dataset: key, note: b.Notes})
			}
		}

		rows = append(rows, []string{info.Type, info.Name, desc, strings.Join(datasets, ", ")})
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i][0] != rows[j][0] {
			return rows[i][0] < rows[j][0]
		}

		return rows[i][1] < rows[j][1]
	})

	printTable([]string{"TYPE", "NAME", "DESCRIPTION", "DATASETS"}, rows)

	if len(notes) > 0 {
		fmt.Println("\nNotes:")

		for _, n := range notes {
			fmt.Printf("  %s/%s: %s\n", n.source, n.dataset, n.note)
		}
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
