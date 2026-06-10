package prometheus

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"sync"

	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/types"
)

// Compile-time interface checks.
var (
	_ module.Module                 = (*Module)(nil)
	_ module.ProxyDiscoverable      = (*Module)(nil)
	_ module.SandboxEnvProvider     = (*Module)(nil)
	_ module.DatasourceInfoProvider = (*Module)(nil)
	_ module.ExamplesProvider       = (*Module)(nil)
	_ module.PythonAPIDocsProvider  = (*Module)(nil)
)

// Module implements the module.Module interface for Prometheus.
type Module struct {
	dsMu        sync.RWMutex
	datasources []types.DatasourceInfo
}

// New creates a new Prometheus module.
func New() *Module { return &Module{} }

func (m *Module) Name() string { return "prometheus" }

// InitFromDiscovery initializes the module from discovered datasources.
// Safe to call repeatedly: subsequent calls replace the datasource list in
// place so the proxy client's periodic refresh propagates without a restart.
//
// Always writes the filtered list, including when empty — see the comment on
// the clickhouse module's InitFromDiscovery for the rationale.
func (m *Module) InitFromDiscovery(datasources []types.DatasourceInfo) error {
	filtered := make([]types.DatasourceInfo, 0, len(datasources))

	for _, ds := range datasources {
		if ds.Type != "prometheus" {
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

// Init is a no-op: the module's datasources come from proxy discovery via
// InitFromDiscovery.
func (m *Module) Init(_ []byte) error { return nil }

// ApplyDefaults sets default values before validation.
func (m *Module) ApplyDefaults() {}

// Validate checks that the parsed config is valid.
func (m *Module) Validate() error {
	m.dsMu.RLock()
	defer m.dsMu.RUnlock()

	names := make(map[string]struct{}, len(m.datasources))
	for i, ds := range m.datasources {
		if ds.Name == "" {
			return fmt.Errorf("datasource[%d].name is required", i)
		}

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

	type datasourceInfo struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}

	infos := make([]datasourceInfo, 0, len(m.datasources))
	for _, ds := range m.datasources {
		infos = append(infos, datasourceInfo{
			Name:        ds.Name,
			Description: ds.Description,
		})
	}

	infosJSON, err := json.Marshal(infos)
	if err != nil {
		return nil, fmt.Errorf("marshaling Prometheus datasource info: %w", err)
	}

	return map[string]string{
		"ETHPANDAOPS_PROMETHEUS_DATASOURCES": string(infosJSON),
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

// Examples returns query examples for the Prometheus module.
func (m *Module) Examples() map[string]types.ExampleCategory {
	result := make(map[string]types.ExampleCategory, len(queryExamples))
	maps.Copy(result, queryExamples)

	return result
}

// PythonAPIDocs returns the Prometheus module documentation.
func (m *Module) PythonAPIDocs() map[string]types.ModuleDoc {
	return map[string]types.ModuleDoc{
		"prometheus": {
			Description: "Query Prometheus metrics",
			Functions: map[string]types.FunctionDoc{
				"list_datasources": {
					Signature:   "prometheus.list_datasources() -> list[dict]",
					Description: "List available Prometheus datasources. Prefer datasources://prometheus resource.",
					Returns:     "List of dicts with 'name', 'description', 'url', 'type', 'extra' keys",
				},
				"query": {
					Signature:   "prometheus.query(instance_name: str, promql: str, time: str = None) -> dict",
					Description: "Execute instant PromQL query. Datasource names, metric names, and labels are deployment-specific; call list_datasources and use get_label_values(instance_name, \"__name__\", contains=\"...\", limit=50) for concise metric discovery.",
					Parameters: map[string]string{
						"instance_name": "Datasource name from datasources://prometheus",
						"promql":        "PromQL query string",
						"time":          "Optional: RFC3339, unix timestamp, or 'now-1h' format",
					},
					Returns: "Dict with 'resultType' and 'result' keys",
				},
				"query_range": {
					Signature:   "prometheus.query_range(instance_name: str, promql: str, step: str, start: str = None, end: str = None) -> dict",
					Description: "Execute range PromQL query",
					Parameters: map[string]string{
						"instance_name": "Datasource name",
						"promql":        "PromQL query string",
						"step":          "Resolution step (e.g., '1m', '5m')",
						"start":         "Start time (default: now-1h)",
						"end":           "End time (default: now)",
					},
					Returns: "Dict with time series data",
				},
				"get_labels": {
					Signature:   "prometheus.get_labels(instance_name: str, start: str = None, end: str = None) -> list[str]",
					Description: "Get all label names",
					Parameters: map[string]string{
						"instance_name": "Datasource name",
						"start":         "Optional start time",
						"end":           "Optional end time",
					},
					Returns: "List of label names",
				},
				"get_label_values": {
					Signature:   "prometheus.get_label_values(instance_name: str, label: str, start: str = None, end: str = None, contains: str = None, limit: int = None) -> list[str]",
					Description: "Get values for a label. Use label=\"__name__\" with contains and limit to discover metric names before writing PromQL against an unfamiliar datasource without dumping every metric.",
					Parameters: map[string]string{
						"instance_name": "Datasource name",
						"label":         "Label name",
						"start":         "Optional start time",
						"end":           "Optional end time",
						"contains":      "Optional case-insensitive substring filter applied locally",
						"limit":         "Optional maximum number of values returned after filtering",
					},
					Returns: "List of label values",
				},
			},
		},
	}
}

// Start performs async initialization.
func (m *Module) Start(_ context.Context) error { return nil }

// Stop cleans up resources.
func (m *Module) Stop(_ context.Context) error { return nil }
