package clickhouse

import (
	"context"
	"fmt"
	"maps"
	"time"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"

	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/proxy"
	"github.com/ethpandaops/panda/pkg/types"
)

// Compile-time interface checks.
var (
	_ module.Module            = (*Module)(nil)
	_ module.ProxyDiscoverable = (*Module)(nil)
)

// Module implements the module.Module interface for ClickHouse.
type Module struct {
	cfg          Config
	datasources  []types.DatasourceInfo
	examples     map[string]types.ExampleCategory
	log          logrus.FieldLogger
	schemaClient ClickHouseSchemaClient
	proxySvc     proxy.ClickHouseSchemaAccess
}

// New creates a new ClickHouse module.
func New() *Module {
	return &Module{}
}

func (ext *Module) Name() string { return "clickhouse" }

// SchemaClient returns the schema discovery client, or nil if not initialized.
func (ext *Module) SchemaClient() ClickHouseSchemaClient { return ext.schemaClient }

// SetProxyClient injects the proxy collaborator used for schema discovery.
func (ext *Module) SetProxyClient(client proxy.ClickHouseSchemaAccess) {
	ext.proxySvc = client
}

// InitFromDiscovery initializes the module from discovered datasources.
func (ext *Module) InitFromDiscovery(datasources []types.DatasourceInfo) error {
	var filtered []types.DatasourceInfo

	for _, ds := range datasources {
		if ds.Type != "clickhouse" {
			continue
		}

		filtered = append(filtered, ds)
	}

	if len(filtered) == 0 {
		return module.ErrNoValidConfig
	}

	ext.datasources = filtered

	return nil
}

// Init parses the raw YAML config for this module.
func (ext *Module) Init(rawConfig []byte) error {
	if err := yaml.Unmarshal(rawConfig, &ext.cfg); err != nil {
		return err
	}

	// Drop schema discovery entries without a datasource name.
	validDatasources := make([]SchemaDiscoveryDatasource, 0, len(ext.cfg.SchemaDiscovery.Datasources))
	for _, ds := range ext.cfg.SchemaDiscovery.Datasources {
		if ds.Name != "" {
			validDatasources = append(validDatasources, ds)
		}
	}

	ext.cfg.SchemaDiscovery.Datasources = validDatasources

	return nil
}

// ApplyDefaults sets default values before validation.
func (ext *Module) ApplyDefaults() {
	if ext.cfg.SchemaDiscovery.RefreshInterval == 0 {
		ext.cfg.SchemaDiscovery.RefreshInterval = 15 * time.Minute
	}
}

// Validate checks that the parsed config is valid.
func (ext *Module) Validate() error {
	if err := ext.ensureExamplesLoaded(); err != nil {
		return err
	}

	for i, ds := range ext.cfg.SchemaDiscovery.Datasources {
		if ds.Name == "" {
			return fmt.Errorf("schema_discovery.datasources[%d].name is required", i)
		}
	}

	// Validate datasources have unique names.
	names := make(map[string]struct{}, len(ext.datasources))
	for i, ds := range ext.datasources {
		if _, exists := names[ds.Name]; exists {
			return fmt.Errorf("datasource[%d].name %q is duplicated", i, ds.Name)
		}

		names[ds.Name] = struct{}{}
	}

	return nil
}

// Examples returns query examples for the ClickHouse module.
func (ext *Module) Examples() map[string]types.ExampleCategory {
	result := make(map[string]types.ExampleCategory, len(ext.examples))
	maps.Copy(result, ext.examples)

	return result
}

func (ext *Module) ensureExamplesLoaded() error {
	if ext.examples != nil {
		return nil
	}

	examples, err := loadExamples()
	if err != nil {
		return err
	}

	ext.examples = examples

	return nil
}

// PythonAPIDocs returns the ClickHouse module documentation.
func (ext *Module) PythonAPIDocs() map[string]types.ModuleDoc {
	return map[string]types.ModuleDoc{
		"clickhouse": {
			Description: "Query ClickHouse databases for Ethereum blockchain data. Use the search tool with type='examples' for query patterns.",
			Functions: map[string]types.FunctionDoc{
				"list_datasources": {
					Signature:   "clickhouse.list_datasources() -> list[dict]",
					Description: "List available ClickHouse datasources. Prefer datasources://clickhouse resource instead.",
					Returns:     "List of dicts with 'name', 'description', 'database' keys",
				},
				"query": {
					Signature:   "clickhouse.query(datasource: str, sql: str) -> pandas.DataFrame",
					Description: "Execute SQL query, return DataFrame",
					Parameters: map[string]string{
						"datasource": "'xatu' or 'xatu-cbt' - see panda://getting-started for syntax differences",
						"sql":        "SQL query string",
					},
					Returns: "pandas.DataFrame",
				},
				"query_raw": {
					Signature:   "clickhouse.query_raw(datasource: str, sql: str) -> tuple[list[tuple], list[str]]",
					Description: "Execute SQL query, return raw tuples",
					Parameters: map[string]string{
						"datasource": "'xatu' or 'xatu-cbt'",
						"sql":        "SQL query string",
					},
					Returns: "(rows, column_names)",
				},
			},
		},
	}
}

// GettingStartedSnippet returns ClickHouse-specific getting-started content.
func (ext *Module) GettingStartedSnippet() string {
	return `## ClickHouse Cluster Rules

Xatu data is split across **TWO clusters** with **DIFFERENT syntax**:

| Cluster | Contains | Table Syntax | Network Filter |
|---------|----------|--------------|----------------|
| **xatu** | Raw events | ` + "`FROM table_name`" + ` | ` + "`WHERE meta_network_name = 'mainnet'`" + ` |
| **xatu-cbt** | Pre-aggregated | ` + "`FROM mainnet.table_name`" + ` | Database prefix IS the filter |

**Always filter by partition column** (usually ` + "`slot_start_date_time`" + `) to avoid timeouts.

## Canonical vs Head Data

- **Canonical** = finalized (no reorgs) - use for historical analysis
- **Head** = latest (may reorg) - use for real-time monitoring
- Tables have variants: ` + "`fct_block_canonical`" + ` vs ` + "`fct_block_head`"
}

// RegisterResources registers ClickHouse schema resources.
func (ext *Module) RegisterResources(log logrus.FieldLogger, reg module.ResourceRegistry) error {
	ext.log = log.WithField("module", "clickhouse")
	if ext.schemaClient != nil {
		RegisterSchemaResources(ext.log, reg, ext.schemaClient)
	}

	return nil
}

// Start performs async initialization (schema discovery).
func (ext *Module) Start(ctx context.Context) error {
	if ext.log == nil {
		ext.log = logrus.WithField("module", "clickhouse")
	}

	if ext.cfg.SchemaDiscovery.Enabled != nil && !*ext.cfg.SchemaDiscovery.Enabled {
		ext.log.Debug("Schema discovery disabled, skipping")

		return nil
	}

	if ext.proxySvc == nil {
		return fmt.Errorf("proxy service is required for ClickHouse schema discovery")
	}

	datasources := make([]SchemaDiscoveryDatasource, 0, len(ext.cfg.SchemaDiscovery.Datasources))
	for _, ds := range ext.cfg.SchemaDiscovery.Datasources {
		if ds.Name == "" {
			continue
		}

		if ds.Cluster == "" {
			ds.Cluster = ds.Name
		}

		datasources = append(datasources, ds)
	}

	if len(datasources) == 0 {
		for _, name := range ext.proxySvc.ClickHouseDatasources() {
			if name == "" {
				continue
			}

			datasources = append(datasources, SchemaDiscoveryDatasource{
				Name:    name,
				Cluster: name,
			})
		}
	}

	if len(datasources) == 0 {
		ext.log.Debug("No ClickHouse datasources available for schema discovery, skipping")

		return nil
	}

	ext.schemaClient = NewClickHouseSchemaClient(
		ext.log,
		ClickHouseSchemaConfig{
			RefreshInterval: ext.cfg.SchemaDiscovery.RefreshInterval,
			QueryTimeout:    DefaultSchemaQueryTimeout,
			Datasources:     datasources,
		},
		ext.proxySvc,
	)

	return ext.schemaClient.Start(ctx)
}

// Stop cleans up resources.
func (ext *Module) Stop(_ context.Context) error {
	if ext.schemaClient != nil {
		return ext.schemaClient.Stop()
	}

	return nil
}
