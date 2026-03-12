package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

var datasourcesType string

var datasourcesCmd = &cobra.Command{
	Use:   "datasources",
	Short: "List available datasources from the server",
	Long: `List all datasources exposed by the configured server, including
ClickHouse clusters, Prometheus instances, and Loki instances.

Examples:
  panda datasources                     # List all datasources
  panda datasources --type clickhouse   # List only ClickHouse clusters
  panda datasources --json              # Output as JSON`,
	RunE: runDatasources,
}

func init() {
	rootCmd.AddCommand(datasourcesCmd)
	datasourcesCmd.Flags().StringVar(&datasourcesType, "type", "", "Filter by type (clickhouse, prometheus, loki)")

	_ = datasourcesCmd.RegisterFlagCompletionFunc("type", cobra.FixedCompletions(
		[]string{"clickhouse", "prometheus", "loki"}, cobra.ShellCompDirectiveNoFileComp,
	))
}

func runDatasources(_ *cobra.Command, _ []string) error {
	ctx := context.Background()
	response, err := listDatasources(ctx, datasourcesType)
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

	for _, info := range response.Datasources {
		desc := info.Description
		if desc == "" {
			desc = info.Name
		}

		fmt.Printf("  %-12s  %-20s  %s\n", info.Type, info.Name, desc)
	}

	return nil
}
