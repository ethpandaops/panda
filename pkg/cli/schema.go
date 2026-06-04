package cli

import (
	"context"
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	clickhousemodule "github.com/ethpandaops/panda/modules/clickhouse"
)

var schemaCmd = &cobra.Command{
	GroupID: groupDiscovery,
	Use:     "schema [cluster] [database] [table]",
	Short:   "Show ClickHouse table schemas",
	Long: `Show available ClickHouse tables and their schemas, scoped by cluster.

Arguments progressively narrow the view:
  panda schema                              list every cluster and its tables
  panda schema <cluster>                    list the tables in one cluster
  panda schema <cluster> <database>         list the tables in one database
  panda schema <cluster> <database> <table> show the full schema for one table

Run 'panda datasources --type clickhouse' (or 'panda schema' with no arguments)
to see the available cluster names.

Examples:
  panda schema
  panda schema clickhouse-raw
  panda schema clickhouse-raw mainnet
  panda schema clickhouse-refined mainnet fct_block_head
  panda schema --json`,
	Args: cobra.MaximumNArgs(3),
	RunE: runSchema,
}

func init() {
	rootCmd.AddCommand(schemaCmd)
	schemaCmd.ValidArgsFunction = completeSchemaArgs
}

func runSchema(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	var (
		response *clickhousemodule.TablesListResponse
		err      error
	)

	switch len(args) {
	case 0:
		response, err = readClickHouseTables(ctx)
	case 1:
		response, err = readClickHouseClusterTables(ctx, args[0])
	case 2:
		response, err = readClickHouseDatabaseTables(ctx, args[0], args[1])
	default:
		return showTable(ctx, args[0], args[1], args[2])
	}

	if err != nil {
		return err
	}

	return renderTablesList(response)
}

func renderTablesList(response *clickhousemodule.TablesListResponse) error {
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

func showTable(ctx context.Context, cluster, database, table string) error {
	response, err := readClickHouseTable(ctx, cluster, database, table)
	if err != nil {
		return err
	}

	if isJSON() {
		return printJSON(response)
	}

	schema := response.Table

	fmt.Printf("Table: %s.%s  (cluster: %s)\n", schema.Database, schema.Name, response.Cluster)

	if schema.Comment != "" {
		fmt.Printf("Comment: %s\n", schema.Comment)
	}

	fmt.Println()

	rows := make([][]string, 0, len(schema.Columns))
	for _, col := range schema.Columns {
		rows = append(rows, []string{col.Name, col.Type, col.Comment})
	}

	printTable([]string{"NAME", "TYPE", "COMMENT"}, rows)

	return nil
}
