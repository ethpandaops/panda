package cli

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

var clickhouseJSON bool

var clickhouseCmd = &cobra.Command{
	Use:   "clickhouse",
	Short: "Query ClickHouse databases",
	Long: `Execute SQL queries against ClickHouse clusters.

Examples:
  ep clickhouse query xatu "SELECT count() FROM beacon_api_eth_v1_events_block WHERE slot_start_date_time > now() - INTERVAL 1 HOUR"
  ep clickhouse query xatu "SELECT * FROM beacon_api_eth_v1_events_block LIMIT 5" --json`,
}

func init() {
	rootCmd.AddCommand(clickhouseCmd)
	clickhouseCmd.PersistentFlags().BoolVar(&clickhouseJSON, "json", false, "Output in JSON format")

	clickhouseCmd.AddCommand(clickhouseQueryCmd)
}

var clickhouseQueryCmd = &cobra.Command{
	Use:   "query <cluster> <sql>",
	Short: "Execute a SQL query",
	Long: `Execute a SQL query against a ClickHouse cluster.

The cluster name is typically "xatu" or "xatu-cbt". Use 'ep datasources --type clickhouse'
to see available clusters.

Examples:
  ep clickhouse query xatu "SELECT count() FROM beacon_api_eth_v1_events_block LIMIT 1"
  ep clickhouse query xatu-cbt "SELECT count() FROM mainnet.beacon_api_eth_v1_events_block LIMIT 1"`,
	Args: cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		ctx := context.Background()

		pc, cleanup, err := startProxy(ctx)
		if err != nil {
			return err
		}

		defer cleanup()

		cluster := args[0]
		sql := args[1]

		// Choose output format based on --json flag.
		format := "TabSeparatedWithNames"
		if clickhouseJSON {
			format = "JSON"
		}

		query := url.Values{"default_format": {format}}

		data, err := proxyPost(ctx, pc, "/clickhouse/",
			strings.NewReader(sql), query, dsHeader(cluster))
		if err != nil {
			return err
		}

		if clickhouseJSON {
			return printJSONBytes(data)
		}

		fmt.Print(string(data))

		return nil
	},
}
