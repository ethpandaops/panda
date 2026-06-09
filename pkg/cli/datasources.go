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

		datasets := make([]string, 0, len(info.Contents))

		for _, b := range info.Contents {
			datasets = append(datasets, b.Dataset)

			if b.Notes != "" {
				notes = append(notes, noteLine{source: info.Name, dataset: b.Dataset, note: b.Notes})
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
