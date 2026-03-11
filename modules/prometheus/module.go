package prometheus

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"

	"gopkg.in/yaml.v3"

	"github.com/ethpandaops/mcp/pkg/module"
	"github.com/ethpandaops/mcp/pkg/types"
)

// Compile-time interface checks.
var (
	_ module.Module            = (*Module)(nil)
	_ module.ProxyDiscoverable = (*Module)(nil)
)

// Module implements the module.Module interface for Prometheus.
type Module struct {
	cfg         Config
	datasources []types.DatasourceInfo
}

// New creates a new Prometheus module.
func New() *Module { return &Module{} }

func (p *Module) Name() string { return "prometheus" }

// InitFromDiscovery initializes the module from discovered datasources.
func (p *Module) InitFromDiscovery(datasources []types.DatasourceInfo) error {
	var filtered []types.DatasourceInfo

	for _, ds := range datasources {
		if ds.Type != "prometheus" {
			continue
		}

		filtered = append(filtered, ds)
	}

	if len(filtered) == 0 {
		return module.ErrNoValidConfig
	}

	p.datasources = filtered

	return nil
}

// Init parses the raw YAML config for this module.
func (p *Module) Init(rawConfig []byte) error {
	if err := yaml.Unmarshal(rawConfig, &p.cfg); err != nil {
		return err
	}

	// Drop unnamed instances.
	validInstances := make([]InstanceConfig, 0, len(p.cfg.Instances))
	for _, inst := range p.cfg.Instances {
		if inst.Name != "" {
			validInstances = append(validInstances, inst)
		}
	}

	p.cfg.Instances = validInstances

	if len(p.cfg.Instances) == 0 {
		return module.ErrNoValidConfig
	}

	// Populate internal datasources from config.
	p.datasources = make([]types.DatasourceInfo, 0, len(p.cfg.Instances))
	for _, inst := range p.cfg.Instances {
		p.datasources = append(p.datasources, types.DatasourceInfo{
			Type:        "prometheus",
			Name:        inst.Name,
			Description: inst.Description,
			Metadata: map[string]string{
				"url": inst.URL,
			},
		})
	}

	return nil
}

// ApplyDefaults sets default values before validation.
func (p *Module) ApplyDefaults() {}

// Validate checks that the parsed config is valid.
func (p *Module) Validate() error {
	names := make(map[string]struct{}, len(p.datasources))
	for i, ds := range p.datasources {
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
func (p *Module) SandboxEnv() (map[string]string, error) {
	if len(p.datasources) == 0 {
		return nil, nil
	}

	type datasourceInfo struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}

	infos := make([]datasourceInfo, 0, len(p.datasources))
	for _, ds := range p.datasources {
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
func (p *Module) DatasourceInfo() []types.DatasourceInfo {
	result := make([]types.DatasourceInfo, len(p.datasources))
	copy(result, p.datasources)

	return result
}

// Examples returns query examples for the Prometheus module.
func (p *Module) Examples() map[string]types.ExampleCategory {
	result := make(map[string]types.ExampleCategory, len(queryExamples))
	maps.Copy(result, queryExamples)

	return result
}

// PythonAPIDocs returns the Prometheus module documentation.
func (p *Module) PythonAPIDocs() map[string]types.ModuleDoc {
	return map[string]types.ModuleDoc{
		"prometheus": {
			Description: "Query Prometheus metrics",
			Functions: map[string]types.FunctionDoc{
				"list_datasources": {
					Signature:   "prometheus.list_datasources() -> list[dict]",
					Description: "List available Prometheus datasources. Prefer datasources://prometheus resource.",
					Returns:     "List of dicts with 'name', 'description', 'url' keys",
				},
				"query": {
					Signature:   "prometheus.query(datasource: str, promql: str, time: str = None) -> dict",
					Description: "Execute instant PromQL query",
					Parameters: map[string]string{
						"datasource": "Datasource name from datasources://prometheus",
						"promql":     "PromQL query string",
						"time":       "Optional: RFC3339, unix timestamp, or 'now-1h' format",
					},
					Returns: "Dict with 'resultType' and 'result' keys",
				},
				"query_range": {
					Signature:   "prometheus.query_range(datasource: str, promql: str, start: str, end: str, step: str) -> dict",
					Description: "Execute range PromQL query",
					Parameters: map[string]string{
						"datasource": "Datasource name",
						"promql":     "PromQL query string",
						"start":      "Start time (RFC3339, unix, or 'now-1h')",
						"end":        "End time (RFC3339, unix, or 'now')",
						"step":       "Resolution step (e.g., '1m', '5m')",
					},
					Returns: "Dict with time series data",
				},
				"get_labels": {
					Signature:   "prometheus.get_labels(datasource: str) -> list[str]",
					Description: "Get all label names",
					Parameters: map[string]string{
						"datasource": "Datasource name",
					},
					Returns: "List of label names",
				},
				"get_label_values": {
					Signature:   "prometheus.get_label_values(datasource: str, label: str) -> list[str]",
					Description: "Get all values for a label",
					Parameters: map[string]string{
						"datasource": "Datasource name",
						"label":      "Label name",
					},
					Returns: "List of label values",
				},
			},
		},
	}
}

// Start performs async initialization.
func (p *Module) Start(_ context.Context) error { return nil }

// Stop cleans up resources.
func (p *Module) Stop(_ context.Context) error { return nil }
