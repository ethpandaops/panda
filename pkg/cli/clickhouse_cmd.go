package cli

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"regexp"

	"github.com/spf13/cobra"
)

// ansiEscape matches ANSI escape sequences (CSI colour/style codes and OSC
// strings) that clients embed in log bodies. ClickHouse stores these bytes
// verbatim, so they leak into text and JSON output unless stripped.
var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)

// stripANSI controls whether ANSI escape sequences are removed from
// ClickHouse query output. Enabled by default so log bodies render cleanly.
var stripANSI = true

var clickhouseCmd = &cobra.Command{
	GroupID: groupDirect,
	Use:     "clickhouse",
	Short:   "Query ClickHouse databases",
	Long: `Execute SQL queries against ClickHouse datasources.

Datasource names come from 'panda datasources'; query syntax rules for a
dataset come from 'panda datasets <name>'. Use 'panda search examples "<topic>"'
for query patterns and 'panda schema' for table names, columns, and keys.

Examples:
  panda clickhouse list-datasources
  panda clickhouse query-raw <datasource> "SHOW DATABASES"
  panda clickhouse <datasource> "SELECT 1"`,
	Args: cobra.MaximumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return cmd.Help()
		}

		if len(args) != 2 {
			return fmt.Errorf("expected <datasource> and <sql>: panda clickhouse <datasource> \"<SQL>\"")
		}

		return runClickHouseOperation(cmd, "clickhouse.query", args[0], args[1], false)
	},
}

func init() {
	rootCmd.AddCommand(clickhouseCmd)

	clickhouseCmd.PersistentFlags().BoolVar(&stripANSI, "strip-ansi", true,
		"strip ANSI escape sequences (terminal colour codes) from output; "+
			"pass --strip-ansi=false to keep them")

	clickhouseCmd.AddCommand(clickhouseListDatasourcesCmd)
	clickhouseCmd.AddCommand(clickhouseQueryCmd)
	clickhouseCmd.AddCommand(clickhouseQueryRawCmd)

	clickhouseQueryCmd.ValidArgsFunction = completeDatasourceNames("clickhouse")
	clickhouseQueryRawCmd.ValidArgsFunction = completeDatasourceNames("clickhouse")
}

var clickhouseListDatasourcesCmd = &cobra.Command{
	Use:   "list-datasources",
	Short: "List available ClickHouse datasources",
	RunE: func(cmd *cobra.Command, _ []string) error {
		response, err := runServerOperation(cmd, "clickhouse.list_datasources", map[string]any{})
		if err != nil {
			return err
		}

		return printDatasourceList(response)
	},
}

var clickhouseQueryCmd = &cobra.Command{
	Use:   "query <datasource> <sql>",
	Short: "Execute a SQL query",
	Long: `Execute a SQL query against a ClickHouse datasource.

Datasource names come from 'panda datasources' or 'panda clickhouse list-datasources'.
Read 'panda datasets <name>' for a dataset's query syntax rules, use
'panda search examples "<topic>"' for query patterns, and
'panda schema <cluster> <database> <table>' to inspect a table before querying it.

Examples:
  panda clickhouse query <datasource> "SHOW DATABASES"
  panda clickhouse query-raw <datasource> "SELECT 1"`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runClickHouseOperation(cmd, "clickhouse.query", args[0], args[1], false)
	},
}

var clickhouseQueryRawCmd = &cobra.Command{
	Use:   "query-raw <datasource> <sql>",
	Short: "Execute a SQL query and return raw rows (TSV, or positional JSON with -o json)",
	Long: `Execute a SQL query and return raw rows.

Output follows --output: text (the default) prints ClickHouse's tab-separated
rows verbatim, ideal for piping logs to a pager or grep; -o json returns
positional row arrays under {"columns", "rows"}.

Keep result sets bounded: aggregate in SQL or add a LIMIT when inspecting rows.
For cross-source analysis, run separate bounded queries and combine them with
'panda execute' or another client-side step instead of dumping unbounded rows
through shell JSON.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runClickHouseOperation(cmd, "clickhouse.query_raw", args[0], args[1], true)
	},
}

func runClickHouseOperation(cmd *cobra.Command, operationID, datasource, sql string, raw bool) error {
	response, err := serverOperationRaw(commandContext(cmd), operationID, map[string]any{
		"datasource": datasource,
		"sql":        sql,
	})
	if err != nil {
		return err
	}

	body := response.Body
	if stripANSI {
		body = ansiEscape.ReplaceAll(body, nil)
	}

	// Text output (the default) prints ClickHouse's tab-separated rows verbatim
	// for both query and query-raw, so log bodies pipe straight to a pager or
	// grep. JSON output keeps the two shapes distinct: query-raw emits positional
	// row arrays, query emits column-keyed objects.
	if isJSON() {
		return printClickHouseJSON(body, raw)
	}

	fmt.Print(string(body))
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
