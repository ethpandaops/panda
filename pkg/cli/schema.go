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
	Use:     "schema [database.table]",
	Short:   "Show ClickHouse table schemas",
	Long: `Show available ClickHouse tables and their schemas. Without arguments,
lists all tables. With a qualified "database.table" reference, shows the
full schema including columns, types, and engine.

Examples:
  panda schema
  panda schema mainnet.fct_block_head
  panda schema internal.logs
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

	if isJSON() {
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

			fmt.Printf("  %-60s  %d cols%s\n", table.Database+"."+table.Name, table.ColumnCount, net)
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

	if isJSON() {
		return printJSON(response)
	}

	schema := response.Table

	clusterNames := make([]string, 0, len(response.Clusters))
	for _, c := range response.Clusters {
		clusterNames = append(clusterNames, c.Name)
	}

	fmt.Printf("Table: %s.%s  (clusters: %s)\n", schema.Database, schema.Name, strings.Join(clusterNames, ", "))

	if schema.Comment != "" {
		fmt.Printf("Comment: %s\n", schema.Comment)
	}

	for _, c := range response.Clusters {
		fmt.Printf("  %s -> %s.%s\n", c.Name, c.Database, schema.Name)
	}

	fmt.Println()

	rows := make([][]string, 0, len(schema.Columns))
	for _, col := range schema.Columns {
		rows = append(rows, []string{col.Name, col.Type, col.Comment})
	}

	printTable([]string{"NAME", "TYPE", "COMMENT"}, rows)

	return nil
}
