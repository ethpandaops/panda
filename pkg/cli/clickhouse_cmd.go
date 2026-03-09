package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ethpandaops/mcp/pkg/operations"
)

var clickhouseJSON bool

var clickhouseCmd = &cobra.Command{
	Use:   "clickhouse",
	Short: "Query ClickHouse databases",
	Long: `Execute SQL queries against ClickHouse clusters.

Examples:
  ep clickhouse list-datasources
  ep clickhouse query xatu "SELECT count() FROM beacon_api_eth_v1_events_block WHERE slot_start_date_time > now() - INTERVAL 1 HOUR"
  ep clickhouse query xatu "SELECT * FROM beacon_api_eth_v1_events_block LIMIT 5" --json`,
}

func init() {
	rootCmd.AddCommand(clickhouseCmd)
	clickhouseCmd.PersistentFlags().BoolVar(&clickhouseJSON, "json", false, "Output in JSON format")

	clickhouseCmd.AddCommand(clickhouseListDatasourcesCmd)
	clickhouseCmd.AddCommand(clickhouseQueryCmd)
	clickhouseCmd.AddCommand(clickhouseQueryRawCmd)
}

var clickhouseListDatasourcesCmd = &cobra.Command{
	Use:   "list-datasources",
	Short: "List available ClickHouse clusters",
	RunE: func(_ *cobra.Command, _ []string) error {
		ctx := context.Background()

		pc, cleanup, err := startProxy(ctx)
		if err != nil {
			return err
		}
		defer cleanup()

		response, err := proxyOperation(ctx, pc, "clickhouse.list_datasources", map[string]any{})
		if err != nil {
			return err
		}

		if clickhouseJSON {
			return printJSON(response)
		}

		data, _ := response.Data.(map[string]any)
		items, _ := data["datasources"].([]any)
		if len(items) == 0 {
			fmt.Println("No ClickHouse datasources found.")
			return nil
		}

		for _, item := range items {
			ds, _ := item.(map[string]any)
			name, _ := ds["name"].(string)
			desc, _ := ds["description"].(string)
			database, _ := ds["database"].(string)

			if desc == "" {
				desc = name
			}

			if database != "" {
				fmt.Printf("  %-16s  %-20s  database=%s\n", name, desc, database)
				continue
			}

			fmt.Printf("  %-16s  %s\n", name, desc)
		}

		return nil
	},
}

var clickhouseQueryCmd = &cobra.Command{
	Use:   "query <cluster> <sql>",
	Short: "Execute a SQL query",
	Long: `Execute a SQL query against a ClickHouse cluster.

The cluster name is typically "xatu" or "xatu-cbt". Use 'ep clickhouse list-datasources'
to see available clusters.

Examples:
  ep clickhouse query xatu "SELECT count() FROM beacon_api_eth_v1_events_block LIMIT 1"
  ep clickhouse query xatu-cbt "SELECT count() FROM mainnet.beacon_api_eth_v1_events_block LIMIT 1"`,
	Args: cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		return runClickHouseOperation("clickhouse.query", args[0], args[1], false)
	},
}

var clickhouseQueryRawCmd = &cobra.Command{
	Use:   "query-raw <cluster> <sql>",
	Short: "Execute a SQL query and return raw rows",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		return runClickHouseOperation("clickhouse.query_raw", args[0], args[1], true)
	},
}

func runClickHouseOperation(operationID, cluster, sql string, raw bool) error {
	ctx := context.Background()

	pc, cleanup, err := startProxy(ctx)
	if err != nil {
		return err
	}
	defer cleanup()

	response, err := proxyOperation(ctx, pc, operationID, map[string]any{
		"cluster": cluster,
		"sql":     sql,
	})
	if err != nil {
		return err
	}

	if clickhouseJSON || raw {
		return printJSON(response)
	}

	return printOperationTable(response)
}

func printOperationTable(response *operations.Response) error {
	if response.Kind != operations.ResultKindTable {
		return printJSON(response)
	}

	if len(response.Columns) > 0 {
		fmt.Println(strings.Join(response.Columns, "\t"))
	}

	for _, row := range response.Rows {
		values := make([]string, 0, len(response.Columns))
		for _, column := range response.Columns {
			values = append(values, fmt.Sprint(row[column]))
		}
		fmt.Println(strings.Join(values, "\t"))
	}

	return nil
}
