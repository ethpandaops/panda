package clickhouse

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"

	"github.com/ethpandaops/mcp/pkg/module"
	"github.com/ethpandaops/mcp/pkg/proxy"
	"github.com/ethpandaops/mcp/pkg/types"
)

// Compile-time interface check
var _ module.Module = (*Module)(nil)

type Module struct {
	cfg          Config
	log          logrus.FieldLogger
	schemaClient ClickHouseSchemaClient
	proxySvc     proxy.Service
}

func New() *Module {
	return &Module{}
}

func (p *Module) Name() string { return "clickhouse" }

// SchemaClient returns the schema discovery client, or nil if not initialized.
func (p *Module) SchemaClient() ClickHouseSchemaClient { return p.schemaClient }

// SetProxyClient injects the proxy service for schema discovery.
func (p *Module) SetProxyClient(client proxy.Service) {
	p.proxySvc = client
}

func (p *Module) Init(rawConfig []byte) error {
	if err := yaml.Unmarshal(rawConfig, &p.cfg); err != nil {
		return err
	}

	// Drop unnamed clusters; remaining fields are optional when proxy is authoritative.
	validClusters := make([]ClusterConfig, 0, len(p.cfg.Clusters))
	for _, c := range p.cfg.Clusters {
		if c.Name != "" {
			validClusters = append(validClusters, c)
		}
	}
	p.cfg.Clusters = validClusters

	// Drop schema discovery entries without a datasource name.
	validDatasources := make([]SchemaDiscoveryDatasource, 0, len(p.cfg.SchemaDiscovery.Datasources))
	for _, ds := range p.cfg.SchemaDiscovery.Datasources {
		if ds.Name != "" {
			validDatasources = append(validDatasources, ds)
		}
	}
	p.cfg.SchemaDiscovery.Datasources = validDatasources

	return nil
}

func (p *Module) ApplyDefaults() {
	for i := range p.cfg.Clusters {
		if p.cfg.Clusters[i].Timeout == 0 {
			p.cfg.Clusters[i].Timeout = 120
		}
	}
	if p.cfg.SchemaDiscovery.RefreshInterval == 0 {
		p.cfg.SchemaDiscovery.RefreshInterval = 15 * time.Minute
	}
}

func (p *Module) Validate() error {
	names := make(map[string]struct{}, len(p.cfg.Clusters))
	for i, ch := range p.cfg.Clusters {
		if ch.Name == "" {
			return fmt.Errorf("clusters[%d].name is required", i)
		}
		if _, exists := names[ch.Name]; exists {
			return fmt.Errorf("clusters[%d].name %q is duplicated", i, ch.Name)
		}
		names[ch.Name] = struct{}{}
	}
	// Validate schema discovery entries.
	for i, ds := range p.cfg.SchemaDiscovery.Datasources {
		if ds.Name == "" {
			return fmt.Errorf("schema_discovery.datasources[%d].name is required", i)
		}
	}
	return nil
}

// SandboxEnv returns credential-free environment variables for the sandbox.
// Credentials are never passed to sandbox containers - they connect via
// the credential proxy instead.
func (p *Module) SandboxEnv() (map[string]string, error) {
	if len(p.cfg.Clusters) == 0 {
		return nil, nil
	}

	// Return datasource info without credentials.
	type datasourceInfo struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Database    string `json:"database"`
	}

	infos := make([]datasourceInfo, 0, len(p.cfg.Clusters))
	for _, cluster := range p.cfg.Clusters {
		infos = append(infos, datasourceInfo{
			Name:        cluster.Name,
			Description: cluster.Description,
			Database:    cluster.Database,
		})
	}

	infosJSON, err := json.Marshal(infos)
	if err != nil {
		return nil, fmt.Errorf("marshaling ClickHouse datasource info: %w", err)
	}

	return map[string]string{
		"ETHPANDAOPS_CLICKHOUSE_DATASOURCES": string(infosJSON),
	}, nil
}

func (p *Module) DatasourceInfo() []types.DatasourceInfo {
	infos := make([]types.DatasourceInfo, 0, len(p.cfg.Clusters))
	for _, ch := range p.cfg.Clusters {
		infos = append(infos, types.DatasourceInfo{
			Type:        "clickhouse",
			Name:        ch.Name,
			Description: ch.Description,
			Metadata: map[string]string{
				"database": ch.Database,
			},
		})
	}
	return infos
}

func (p *Module) Examples() map[string]types.ExampleCategory {
	result := make(map[string]types.ExampleCategory, len(queryExamples))
	for k, v := range queryExamples {
		result[k] = v
	}
	return result
}

// PythonAPIDocs returns the ClickHouse module documentation.
func (p *Module) PythonAPIDocs() map[string]types.ModuleDoc {
	return map[string]types.ModuleDoc{
		"clickhouse": {
			Description: "Query ClickHouse databases for Ethereum blockchain data. Use the search tool with type='examples' for query patterns.",
			Functions: map[string]types.FunctionDoc{
				"list_datasources": {
					Signature:   "clickhouse.list_datasources() -> list[dict]",
					Description: "List available ClickHouse clusters. Prefer datasources://clickhouse resource instead.",
					Returns:     "List of dicts with 'name', 'description', 'database' keys",
				},
				"query": {
					Signature:   "clickhouse.query(cluster: str, sql: str) -> pandas.DataFrame",
					Description: "Execute SQL query, return DataFrame",
					Parameters: map[string]string{
						"cluster": "'xatu' or 'xatu-cbt' - see ethpandaops://getting-started for syntax differences",
						"sql":     "SQL query string",
					},
					Returns: "pandas.DataFrame",
				},
				"query_raw": {
					Signature:   "clickhouse.query_raw(cluster: str, sql: str) -> tuple[list[tuple], list[str]]",
					Description: "Execute SQL query, return raw tuples",
					Parameters: map[string]string{
						"cluster": "'xatu' or 'xatu-cbt'",
						"sql":     "SQL query string",
					},
					Returns: "(rows, column_names)",
				},
			},
		},
	}
}

// GettingStartedSnippet returns ClickHouse-specific getting-started content.
func (p *Module) GettingStartedSnippet() string {
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

func (p *Module) RegisterResources(log logrus.FieldLogger, reg module.ResourceRegistry) error {
	p.log = log.WithField("module", "clickhouse")
	if p.schemaClient != nil {
		RegisterSchemaResources(p.log, reg, p.schemaClient)
	}
	return nil
}

func (p *Module) Start(ctx context.Context) error {
	// Create the schema client
	if p.log == nil {
		p.log = logrus.WithField("module", "clickhouse")
	}

	if p.cfg.SchemaDiscovery.Enabled != nil && !*p.cfg.SchemaDiscovery.Enabled {
		p.log.Debug("Schema discovery disabled, skipping")
		return nil
	}

	if p.proxySvc == nil {
		return fmt.Errorf("proxy service is required for ClickHouse schema discovery")
	}

	datasources := make([]SchemaDiscoveryDatasource, 0, len(p.cfg.SchemaDiscovery.Datasources))
	for _, ds := range p.cfg.SchemaDiscovery.Datasources {
		if ds.Name == "" {
			continue
		}
		if ds.Cluster == "" {
			ds.Cluster = ds.Name
		}
		datasources = append(datasources, ds)
	}

	if len(datasources) == 0 {
		for _, name := range p.proxySvc.ClickHouseDatasources() {
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
		p.log.Debug("No ClickHouse datasources available for schema discovery, skipping")
		return nil
	}

	p.schemaClient = NewClickHouseSchemaClient(
		p.log,
		ClickHouseSchemaConfig{
			RefreshInterval: p.cfg.SchemaDiscovery.RefreshInterval,
			QueryTimeout:    DefaultSchemaQueryTimeout,
			Datasources:     datasources,
		},
		p.proxySvc,
	)

	return p.schemaClient.Start(ctx)
}

func (p *Module) Stop(_ context.Context) error {
	if p.schemaClient != nil {
		return p.schemaClient.Stop()
	}
	return nil
}
