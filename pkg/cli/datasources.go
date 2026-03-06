package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ethpandaops/mcp/pkg/app"
	"github.com/ethpandaops/mcp/pkg/config"
	"github.com/ethpandaops/mcp/pkg/proxy"
	"github.com/ethpandaops/mcp/pkg/types"
)

var (
	datasourcesType string
	datasourcesJSON bool
)

var datasourcesCmd = &cobra.Command{
	Use:   "datasources",
	Short: "List available datasources from the proxy",
	Long: `List all datasources discovered from the credential proxy, including
ClickHouse clusters, Prometheus instances, and Loki instances.

Examples:
  ep datasources                     # List all datasources
  ep datasources --type clickhouse   # List only ClickHouse clusters
  ep datasources --json              # Output as JSON`,
	RunE: runDatasources,
}

func init() {
	rootCmd.AddCommand(datasourcesCmd)
	datasourcesCmd.Flags().StringVar(&datasourcesType, "type", "", "Filter by type (clickhouse, prometheus, loki)")
	datasourcesCmd.Flags().BoolVar(&datasourcesJSON, "json", false, "Output in JSON format")
}

func runDatasources(_ *cobra.Command, _ []string) error {
	ctx := context.Background()

	cfg, err := config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	// Only need the proxy client for datasource discovery.
	proxyClient := buildProxyClient(cfg)
	if err := proxyClient.Start(ctx); err != nil {
		return fmt.Errorf("connecting to proxy: %w", err)
	}

	defer func() { _ = proxyClient.Stop(ctx) }()

	infos := collectDatasourceInfo(proxyClient, datasourcesType)

	if datasourcesJSON {
		return printJSON(map[string]any{"datasources": infos})
	}

	if len(infos) == 0 {
		fmt.Println("No datasources found.")

		return nil
	}

	for _, info := range infos {
		desc := info.Description
		if desc == "" {
			desc = info.Name
		}

		fmt.Printf("  %-12s  %-20s  %s\n", info.Type, info.Name, desc)
	}

	return nil
}

// buildProxyClient creates a proxy client from config. Shared by multiple commands.
func buildProxyClient(cfg *config.Config) proxy.Client {
	proxyCfg := proxy.ClientConfig{
		URL: cfg.Proxy.URL,
	}

	if cfg.Proxy.Auth != nil {
		proxyCfg.IssuerURL = cfg.Proxy.Auth.IssuerURL
		proxyCfg.ClientID = cfg.Proxy.Auth.ClientID
	}

	return proxy.NewClient(log, proxyCfg)
}

// buildApp creates and builds the full App from config. Shared by commands needing all components.
func buildApp(ctx context.Context, cfg *config.Config) (*app.App, error) {
	a := app.New(log, cfg)
	if err := a.Build(ctx); err != nil {
		return nil, err
	}

	return a, nil
}

func collectDatasourceInfo(proxyClient proxy.Client, filterType string) []types.DatasourceInfo {
	var all []types.DatasourceInfo

	all = append(all, proxyClient.ClickHouseDatasourceInfo()...)
	all = append(all, proxyClient.PrometheusDatasourceInfo()...)
	all = append(all, proxyClient.LokiDatasourceInfo()...)

	if filterType == "" {
		return all
	}

	var filtered []types.DatasourceInfo
	for _, info := range all {
		if info.Type == filterType {
			filtered = append(filtered, info)
		}
	}

	return filtered
}

// printJSON marshals v as indented JSON and prints it.
func printJSON(v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling JSON: %w", err)
	}

	fmt.Println(string(data))

	return nil
}
