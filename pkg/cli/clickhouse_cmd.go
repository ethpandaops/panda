package cli

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"

	"github.com/spf13/cobra"
)

var clickhouseCmd = &cobra.Command{
	GroupID: groupDirect,
	Use:     "clickhouse",
	Short:   "Query ClickHouse databases",
	Long: `Execute SQL queries against ClickHouse clusters.

Examples:
  panda clickhouse list-datasources
  panda clickhouse query xatu "SELECT count() FROM beacon_api_eth_v1_events_block WHERE slot_start_date_time > now() - INTERVAL 1 HOUR"
  panda clickhouse query xatu "SELECT * FROM beacon_api_eth_v1_events_block LIMIT 5" --json`,
}

func init() {
	rootCmd.AddCommand(clickhouseCmd)

	clickhouseCmd.AddCommand(clickhouseListDatasourcesCmd)
	clickhouseCmd.AddCommand(clickhouseQueryCmd)
	clickhouseCmd.AddCommand(clickhouseQueryRawCmd)

	clickhouseQueryCmd.ValidArgsFunction = completeDatasourceNames("clickhouse")
	clickhouseQueryRawCmd.ValidArgsFunction = completeDatasourceNames("clickhouse")
}

var clickhouseListDatasourcesCmd = &cobra.Command{
	Use:   "list-datasources",
	Short: "List available ClickHouse clusters",
	RunE: func(_ *cobra.Command, _ []string) error {
		response, err := runServerOperation("clickhouse.list_datasources", map[string]any{})
		if err != nil {
			return err
		}

		return printDatasourceList(response)
	},
}

var clickhouseQueryCmd = &cobra.Command{
	Use:   "query <cluster> <sql>",
	Short: "Execute a SQL query",
	Long: `Execute a SQL query against a ClickHouse cluster.

The cluster name is typically "xatu" or "xatu-cbt". Use 'panda clickhouse list-datasources'
to see available clusters.

Examples:
  panda clickhouse query xatu "SELECT count() FROM beacon_api_eth_v1_events_block LIMIT 1"
  panda clickhouse query xatu-cbt "SELECT count() FROM mainnet.beacon_api_eth_v1_events_block LIMIT 1"`,
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

	response, err := serverOperationRaw(ctx, operationID, map[string]any{
		"cluster": cluster,
		"sql":     sql,
	})
	if err != nil {
		return err
	}

	if raw {
		return printClickHouseJSON(response.Body, true)
	}

	if isJSON() {
		return printClickHouseJSON(response.Body, false)
	}

	fmt.Print(string(response.Body))
	return nil
}

func printClickHouseJSON(data []byte, raw bool) error {
	columns, rows, err := parseClickHouseTSV(data)
	if err != nil {
		return fmt.Errorf("parsing ClickHouse TSV response: %w", err)
	}

	if raw {
		matrix := make([][]string, 0, len(rows))
		matrix = append(matrix, rows...)

		return printJSON(map[string]any{
			"columns": columns,
			"rows":    matrix,
		})
	}

	items := make([]map[string]string, 0, len(rows))
	for _, row := range rows {
		item := make(map[string]string, len(columns))
		for idx, column := range columns {
			if idx < len(row) {
				item[column] = row[idx]
			}
		}

		items = append(items, item)
	}

	return printJSON(map[string]any{
		"columns": columns,
		"rows":    items,
	})
}

func parseClickHouseTSV(data []byte) ([]string, [][]string, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, nil, nil
	}

	reader := csv.NewReader(bytes.NewReader(trimmed))
	reader.Comma = '\t'
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true

	records, err := reader.ReadAll()
	if err != nil {
		return nil, nil, err
	}
	if len(records) == 0 {
		return nil, nil, nil
	}

	return records[0], records[1:], nil
}
