package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

var schemaCmd = &cobra.Command{
	GroupID: groupDiscovery,
	Use:     "schema [table-name]",
	Short:   "Show ClickHouse table schemas",
	Long: `Show available ClickHouse tables and their schemas. Without arguments,
lists all tables. With a table name, shows the full schema including
columns, types, and available networks.

Examples:
  panda schema
  panda schema beacon_api_eth_v1_events_block
  panda schema --json`,
	RunE: runSchema,
}

func init() {
	rootCmd.AddCommand(schemaCmd)
	schemaCmd.ValidArgsFunction = completeTableNames
}

func runSchema(_ *cobra.Command, args []string) error {
	ctx := context.Background()

	if len(args) == 0 {
		return listTables(ctx)
	}

	return showTable(ctx, args[0])
}

func listTables(ctx context.Context) error {
	response, err := readClickHouseTables(ctx)
	if err != nil {
		return err
	}

	if schemaJSON || isJSON() {
		return printJSON(response)
	}

	clusterNames := make([]string, 0, len(response.Clusters))
	for clusterName := range response.Clusters {
		clusterNames = append(clusterNames, clusterName)
	}
	sort.Strings(clusterNames)

	for _, clusterName := range clusterNames {
		cluster := response.Clusters[clusterName]
		fmt.Printf("Cluster: %s (%d tables, updated %s)\n", clusterName, cluster.TableCount, cluster.LastUpdated)

		for _, table := range cluster.Tables {
			net := ""
			if table.HasNetworkCol {
				net = " (network-filtered)"
			}

			fmt.Printf("  %-50s  %d cols%s\n", table.Name, table.ColumnCount, net)
		}

		fmt.Println()
	}

	return nil
}

func showTable(ctx context.Context, tableName string) error {
	response, err := readClickHouseTable(ctx, tableName)
	if err != nil {
		return err
	}

	if schemaJSON || isJSON() {
		return printJSON(response)
	}

	schema := response.Table
	fmt.Printf("Table: %s  (cluster: %s)\n", schema.Name, response.Cluster)

	if schema.Comment != "" {
		fmt.Printf("Comment: %s\n", schema.Comment)
	}

	if len(schema.Networks) > 0 {
		fmt.Printf("Networks: %s\n", strings.Join(schema.Networks, ", "))
	}

	fmt.Println()

	rows := make([][]string, 0, len(schema.Columns))
	for _, col := range schema.Columns {
		rows = append(rows, []string{col.Name, col.Type, col.Comment})
	}

	printTable([]string{"NAME", "TYPE", "COMMENT"}, rows)

	return nil
}
