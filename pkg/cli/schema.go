package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ethpandaops/mcp/pkg/config"
	clickhouseplugin "github.com/ethpandaops/mcp/plugins/clickhouse"
)

var schemaJSON bool

var schemaCmd = &cobra.Command{
	Use:   "schema [table-name]",
	Short: "Show ClickHouse table schemas",
	Long: `Show available ClickHouse tables and their schemas. Without arguments,
lists all tables. With a table name, shows the full schema including
columns, types, and available networks.

Examples:
  ep schema                          # List all tables
  ep schema beacon_api_eth_v1_events_block  # Show table schema
  ep schema --json                   # List all tables as JSON`,
	RunE: runSchema,
}

func init() {
	rootCmd.AddCommand(schemaCmd)
	schemaCmd.Flags().BoolVar(&schemaJSON, "json", false, "Output in JSON format")
}

func runSchema(_ *cobra.Command, args []string) error {
	ctx := context.Background()

	cfg, err := config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	schemaClient, cleanup, err := buildSchemaClient(ctx, cfg)
	if err != nil {
		return err
	}

	defer cleanup()

	if schemaClient == nil {
		return fmt.Errorf("no ClickHouse schema discovery configured")
	}

	if len(args) == 0 {
		return listTables(schemaClient)
	}

	return showTable(schemaClient, args[0])
}

func buildSchemaClient(ctx context.Context, cfg *config.Config) (clickhouseplugin.ClickHouseSchemaClient, func(), error) {
	// Build plugin registry.
	a, err := buildLightApp(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}

	cleanup := func() { _ = a.Stop(ctx) }

	// Find the clickhouse plugin and get its schema client.
	p := a.PluginRegistry.Get("clickhouse")
	if p == nil {
		return nil, cleanup, nil
	}

	chPlugin, ok := p.(*clickhouseplugin.Plugin)
	if !ok {
		return nil, cleanup, nil
	}

	return chPlugin.SchemaClient(), cleanup, nil
}

func listTables(client clickhouseplugin.ClickHouseSchemaClient) error {
	allTables := client.GetAllTables()

	if schemaJSON {
		return printJSON(allTables)
	}

	for clusterName, cluster := range allTables {
		fmt.Printf("Cluster: %s (%d tables, updated %s)\n",
			clusterName, len(cluster.Tables), cluster.LastUpdated.Format("2006-01-02 15:04"))

		names := make([]string, 0, len(cluster.Tables))
		for name := range cluster.Tables {
			names = append(names, name)
		}

		sort.Strings(names)

		for _, name := range names {
			t := cluster.Tables[name]
			net := ""
			if t.HasNetworkCol {
				net = " (network-filtered)"
			}

			fmt.Printf("  %-50s  %d cols%s\n", t.Name, len(t.Columns), net)
		}

		fmt.Println()
	}

	return nil
}

func showTable(client clickhouseplugin.ClickHouseSchemaClient, tableName string) error {
	schema, clusterName, found := client.GetTable(tableName)

	// Try case-insensitive match.
	if !found {
		allTables := client.GetAllTables()
		for cluster, ct := range allTables {
			for name, ts := range ct.Tables {
				if strings.EqualFold(name, tableName) {
					schema = ts
					clusterName = cluster
					found = true

					break
				}
			}

			if found {
				break
			}
		}
	}

	if !found {
		return fmt.Errorf("table %q not found", tableName)
	}

	if schemaJSON {
		return printJSON(map[string]any{"table": schema, "cluster": clusterName})
	}

	fmt.Printf("Table: %s  (cluster: %s)\n", schema.Name, clusterName)

	if schema.Comment != "" {
		fmt.Printf("Comment: %s\n", schema.Comment)
	}

	if len(schema.Networks) > 0 {
		fmt.Printf("Networks: %s\n", strings.Join(schema.Networks, ", "))
	}

	fmt.Println()

	data, _ := json.MarshalIndent(schema.Columns, "", "  ")
	fmt.Println(string(data))

	return nil
}
