package cli

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"

	"github.com/ethpandaops/panda/pkg/operations"
	"github.com/spf13/cobra"
)

var clickhouseCmd = &cobra.Command{
	GroupID: groupDirect,
	Use:     "clickhouse",
	Short:   "Query ClickHouse databases",
	Long: `Execute SQL queries against ClickHouse datasources.

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
	Short: "List available ClickHouse datasources",
	RunE: func(_ *cobra.Command, _ []string) error {
		items, err := listClickHouseDatasources()
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSON(operations.DatasourcesPayload{Datasources: items})
		}

		if len(items) == 0 {
			fmt.Println("No ClickHouse datasources found.")
			return nil
		}

		for _, item := range items {
			name := item.Name
			desc := item.Description
			database := item.Database

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
	Use:   "query <datasource> <sql>",
	Short: "Execute a SQL query",
	Long: `Execute a SQL query against a ClickHouse datasource.

The datasource name is typically "xatu" or "xatu-cbt". Use 'panda clickhouse list-datasources'
to see available datasources.

Examples:
  panda clickhouse query xatu "SELECT count() FROM beacon_api_eth_v1_events_block LIMIT 1"
  panda clickhouse query xatu-cbt "SELECT count() FROM mainnet.beacon_api_eth_v1_events_block LIMIT 1"`,
	Args: cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		return runClickHouseQuery(args[0], args[1])
	},
}

var clickhouseQueryRawCmd = &cobra.Command{
	Use:   "query-raw <datasource> <sql>",
	Short: "Execute a SQL query and return raw rows",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		return runClickHouseRawQuery(args[0], args[1])
	},
}

func runClickHouseQuery(datasource, sql string) error {
	ctx := context.Background()

	response, err := clickHouseQuery(ctx, operations.ClickHouseQueryArgs{
		Datasource: datasource,
		SQL:        sql,
	})
	if err != nil {
		return err
	}

	if isJSON() {
		return printClickHouseJSON(response.Body, false)
	}

	fmt.Print(string(response.Body))
	return nil
}

func runClickHouseRawQuery(datasource, sql string) error {
	ctx := context.Background()

	response, err := clickHouseQueryRaw(ctx, operations.ClickHouseQueryArgs{
		Datasource: datasource,
		SQL:        sql,
	})
	if err != nil {
		return err
	}

	return printClickHouseJSON(response.Body, true)
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
