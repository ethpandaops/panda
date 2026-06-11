package clickhouse

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"

	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/proxy"
	"github.com/ethpandaops/panda/pkg/types"
)

// Compile-time interface checks.
var (
	_ module.Module                 = (*Module)(nil)
	_ module.ProxyDiscoverable      = (*Module)(nil)
	_ module.DiscoveryReloadable    = (*Module)(nil)
	_ module.ProxyAware             = (*Module)(nil)
	_ module.ResourceProvider       = (*Module)(nil)
	_ module.SandboxEnvProvider     = (*Module)(nil)
	_ module.DatasourceInfoProvider = (*Module)(nil)
	_ module.PythonAPIDocsProvider  = (*Module)(nil)
)

// Module implements the module.Module interface for ClickHouse.
type Module struct {
	cfg          Config
	dsMu         sync.RWMutex
	datasources  []types.DatasourceInfo
	log          logrus.FieldLogger
	schemaClient SchemaClient
	proxySvc     proxy.Service
}

// New creates a new ClickHouse module.
func New() *Module {
	return &Module{}
}

func (m *Module) Name() string { return "clickhouse" }

// getSchemaClient returns the schema client under the lock: Start can run on
// the discovery-refresh goroutine (late activation) concurrently with readers
// on the main goroutine.
func (m *Module) getSchemaClient() SchemaClient {
	m.dsMu.RLock()
	defer m.dsMu.RUnlock()

	return m.schemaClient
}

// SetProxyClient injects the proxy service for schema discovery.
func (m *Module) SetProxyClient(client proxy.Service) {
	m.proxySvc = client
}

// InitFromDiscovery initializes the module from discovered datasources.
// Safe to call repeatedly: subsequent calls replace the datasource list in
// place so the proxy client's periodic refresh propagates without a restart.
//
// Always writes the filtered list, including when empty. ErrNoValidConfig is
// purely a hint to the registry ("don't activate me at initial init") — an
// already-running module whose datasources have all disappeared still gets
// its list cleared, so panda datasources, sandbox env, and schema discovery
// stop reporting stale entries.
func (m *Module) InitFromDiscovery(datasources []types.DatasourceInfo) error {
	filtered := make([]types.DatasourceInfo, 0, len(datasources))

	for _, ds := range datasources {
		if ds.Type != "clickhouse" {
			continue
		}

		filtered = append(filtered, ds)
	}

	m.dsMu.Lock()
	m.datasources = filtered
	m.dsMu.Unlock()

	if len(filtered) == 0 {
		return module.ErrNoValidConfig
	}

	return nil
}

// OnDiscoveryReloaded pushes the refreshed datasource list into the running
// schema discovery client so newly added ClickHouse clusters get their schemas
// fetched without a server restart. Skipped when YAML schema_discovery.datasources
// is configured (those are authoritative) or when schema discovery is disabled.
func (m *Module) OnDiscoveryReloaded(_ context.Context) error {
	schemaClient := m.getSchemaClient()
	if schemaClient == nil {
		return nil
	}

	// YAML config is authoritative — don't let proxy discovery widen the set.
	if len(m.cfg.SchemaDiscovery.Datasources) > 0 {
		return nil
	}

	m.dsMu.RLock()
	dsList := make([]SchemaDiscoveryDatasource, 0, len(m.datasources))

	for _, ds := range m.datasources {
		if ds.Name == "" {
			continue
		}

		dsList = append(dsList, SchemaDiscoveryDatasource{
			Name:    ds.Name,
			Cluster: ds.Name,
		})
	}
	m.dsMu.RUnlock()

	schemaClient.UpdateDatasources(dsList)

	return nil
}

// Init parses the raw YAML config for this module.
func (m *Module) Init(rawConfig []byte) error {
	if err := yaml.Unmarshal(rawConfig, &m.cfg); err != nil {
		return err
	}

	// Drop schema discovery entries without a datasource name.
	validDatasources := make([]SchemaDiscoveryDatasource, 0, len(m.cfg.SchemaDiscovery.Datasources))
	for _, ds := range m.cfg.SchemaDiscovery.Datasources {
		if ds.Name != "" {
			validDatasources = append(validDatasources, ds)
		}
	}

	m.cfg.SchemaDiscovery.Datasources = validDatasources

	return nil
}

// ApplyDefaults sets default values before validation.
func (m *Module) ApplyDefaults() {
	if m.cfg.SchemaDiscovery.RefreshInterval == 0 {
		m.cfg.SchemaDiscovery.RefreshInterval = DefaultSchemaRefreshInterval
	}

}

// Validate checks that the parsed config is valid.
func (m *Module) Validate() error {
	m.dsMu.RLock()
	defer m.dsMu.RUnlock()

	// Validate datasources have unique names.
	names := make(map[string]struct{}, len(m.datasources))
	for i, ds := range m.datasources {
		if _, exists := names[ds.Name]; exists {
			return fmt.Errorf("datasource[%d].name %q is duplicated", i, ds.Name)
		}

		names[ds.Name] = struct{}{}
	}

	return nil
}

// SandboxEnv returns environment variables for the sandbox.
func (m *Module) SandboxEnv() (map[string]string, error) {
	m.dsMu.RLock()
	defer m.dsMu.RUnlock()

	if len(m.datasources) == 0 {
		return nil, nil
	}

	type datasetInfo struct {
		Dataset string            `json:"dataset"`
		Params  map[string]string `json:"params,omitempty"`
		Notes   string            `json:"notes,omitempty"`
	}

	type datasourceInfo struct {
		Name        string        `json:"name"`
		Description string        `json:"description"`
		Database    string        `json:"database"`
		Datasets    []datasetInfo `json:"datasets,omitempty"`
	}

	infos := make([]datasourceInfo, 0, len(m.datasources))
	for _, ds := range m.datasources {
		var datasets []datasetInfo
		for _, b := range ds.Contents {
			datasets = append(datasets, datasetInfo{Dataset: b.Dataset, Params: b.Params, Notes: b.Notes})
		}

		infos = append(infos, datasourceInfo{
			Name:        ds.Name,
			Description: ds.Description,
			Database:    ds.Metadata["database"],
			Datasets:    datasets,
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

// DatasourceInfo returns datasource metadata for datasources:// resources.
func (m *Module) DatasourceInfo() []types.DatasourceInfo {
	m.dsMu.RLock()
	defer m.dsMu.RUnlock()

	result := make([]types.DatasourceInfo, len(m.datasources))
	copy(result, m.datasources)

	return result
}

// PythonAPIDocs returns the ClickHouse module documentation. It describes the
// generic transport only; dataset-specific guidance (table syntax, conventions)
// lives in the dataset knowledge packs surfaced via search and datasets://{name}.
func (m *Module) PythonAPIDocs() map[string]types.ModuleDoc {
	return map[string]types.ModuleDoc{
		"clickhouse": {
			Description: "Execute SQL against a discovered ClickHouse datasource. Use the search tool for query examples and read datasources://clickhouse for available datasources.",
			Functions: map[string]types.FunctionDoc{
				"list_datasources": {
					Signature:   "clickhouse.list_datasources() -> list[dict]",
					Description: "List available ClickHouse datasources. Prefer datasources://clickhouse resource instead.",
					Returns:     "List of dicts with 'name', 'description', 'url', 'type', 'extra' keys ('extra.database' holds the default database; 'extra.datasets' lists dataset bindings with 'dataset', 'params', 'notes')",
				},
				"query": {
					Signature:   "clickhouse.query(datasource: str, sql: str, parameters: dict | None = None) -> pandas.DataFrame",
					Description: "Execute SQL query and materialize the result as a DataFrame. Keep results bounded with SQL filters, aggregation, or LIMIT before loading them into Python.",
					Parameters: map[string]string{
						"datasource": "a ClickHouse datasource name from clickhouse.list_datasources() or datasources://clickhouse",
						"sql":        "SQL query string; reference tables as database.table and inspect schemas via clickhouse://tables/{cluster}/{database}",
						"parameters": "Optional ClickHouse query parameters referenced in SQL as {name:Type}",
					},
					Returns: "pandas.DataFrame",
				},
				"query_raw": {
					Signature:   "clickhouse.query_raw(datasource: str, sql: str, parameters: dict | None = None) -> tuple[list[tuple], list[str]]",
					Description: "Execute SQL query and materialize raw rows plus column names. Prefer aggregation or LIMIT for row inspection; avoid pulling unbounded tables into the sandbox.",
					Parameters: map[string]string{
						"datasource": "a ClickHouse datasource name from clickhouse.list_datasources() or datasources://clickhouse",
						"sql":        "SQL query string",
						"parameters": "Optional ClickHouse query parameters referenced in SQL as {name:Type}",
					},
					Returns: "(rows, column_names)",
				},
			},
		},
	}
}

// RegisterResources registers ClickHouse schema resources.
func (m *Module) RegisterResources(log logrus.FieldLogger, reg module.ResourceRegistry) error {
	m.log = log.WithField("module", "clickhouse")
	if schemaClient := m.getSchemaClient(); schemaClient != nil {
		RegisterSchemaResources(m.log, reg, schemaClient)
	}

	return nil
}

// Start performs async initialization (schema discovery).
func (m *Module) Start(ctx context.Context) error {
	if m.log == nil {
		m.log = logrus.WithField("module", "clickhouse")
	}

	if m.cfg.SchemaDiscovery.Enabled != nil && !*m.cfg.SchemaDiscovery.Enabled {
		m.log.Debug("Schema discovery disabled, skipping")

		return nil
	}

	if m.proxySvc == nil {
		return fmt.Errorf("proxy service is required for ClickHouse schema discovery")
	}

	datasources := make([]SchemaDiscoveryDatasource, 0, len(m.cfg.SchemaDiscovery.Datasources))
	for _, ds := range m.cfg.SchemaDiscovery.Datasources {
		if ds.Name == "" {
			continue
		}

		if ds.Cluster == "" {
			ds.Cluster = ds.Name
		}

		datasources = append(datasources, ds)
	}

	if len(datasources) == 0 {
		for _, name := range m.proxySvc.ClickHouseDatasources() {
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
		m.log.Debug("No ClickHouse datasources available for schema discovery, skipping")

		return nil
	}

	schemaClient := NewSchemaClient(
		m.log,
		SchemaConfig{
			RefreshInterval: m.cfg.SchemaDiscovery.RefreshInterval,
			QueryTimeout:    DefaultSchemaQueryTimeout,
			Datasources:     datasources,
		},
		m.proxySvc,
	)

	m.dsMu.Lock()
	m.schemaClient = schemaClient
	m.dsMu.Unlock()

	return schemaClient.Start(ctx)
}

// Stop cleans up resources.
func (m *Module) Stop(_ context.Context) error {
	if schemaClient := m.getSchemaClient(); schemaClient != nil {
		return schemaClient.Stop()
	}

	return nil
}
