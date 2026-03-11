package loki

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

// Module implements the module.Module interface for Loki.
type Module struct {
	cfg         Config
	datasources []types.DatasourceInfo
}

// New creates a new Loki module.
func New() *Module { return &Module{} }

func (p *Module) Name() string { return "loki" }

// InitFromDiscovery initializes the module from discovered datasources.
func (p *Module) InitFromDiscovery(datasources []types.DatasourceInfo) error {
	var filtered []types.DatasourceInfo

	for _, ds := range datasources {
		if ds.Type != "loki" {
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
			Type:        "loki",
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
		return nil, fmt.Errorf("marshaling Loki datasource info: %w", err)
	}

	return map[string]string{
		"ETHPANDAOPS_LOKI_DATASOURCES": string(infosJSON),
	}, nil
}

// DatasourceInfo returns datasource metadata for datasources:// resources.
func (p *Module) DatasourceInfo() []types.DatasourceInfo {
	result := make([]types.DatasourceInfo, len(p.datasources))
	copy(result, p.datasources)

	return result
}

// Examples returns query examples for the Loki module.
func (p *Module) Examples() map[string]types.ExampleCategory {
	result := make(map[string]types.ExampleCategory, len(queryExamples))
	maps.Copy(result, queryExamples)

	return result
}

// PythonAPIDocs returns the Loki module documentation.
func (p *Module) PythonAPIDocs() map[string]types.ModuleDoc {
	return map[string]types.ModuleDoc{
		"loki": {
			Description: "Query Loki for log data",
			Functions: map[string]types.FunctionDoc{
				"list_datasources": {
					Signature:   "loki.list_datasources() -> list[dict]",
					Description: "List available Loki datasources. Prefer datasources://loki resource.",
					Returns:     "List of dicts with 'name', 'description', 'url' keys",
				},
				"query": {
					Signature:   "loki.query(datasource: str, logql: str, limit: int = 100, start: str = None, end: str = None, direction: str = 'backward') -> dict",
					Description: "Execute LogQL range query and return the raw Loki data payload",
					Parameters: map[string]string{
						"datasource": "Datasource name from datasources://loki",
						"logql":      "LogQL query string",
						"limit":      "Max entries to return (default: 100)",
						"start":      "Start time (default: now-1h)",
						"end":        "End time (default: now)",
						"direction":  "'forward' or 'backward' (default)",
					},
					Returns: "Dict with Loki stream/vector data under 'resultType' and 'result'",
				},
				"query_instant": {
					Signature:   "loki.query_instant(datasource: str, logql: str, time: str = None, limit: int = 100, direction: str = 'backward') -> dict",
					Description: "Execute instant LogQL query and return the raw Loki data payload",
					Parameters: map[string]string{
						"datasource": "Datasource name",
						"logql":      "LogQL query string",
						"time":       "Evaluation timestamp (default: now)",
						"limit":      "Max entries (default: 100)",
						"direction":  "'forward' or 'backward'",
					},
					Returns: "Dict with Loki stream/vector data under 'resultType' and 'result'",
				},
				"get_labels": {
					Signature:   "loki.get_labels(datasource: str, start: str = None, end: str = None) -> list[str]",
					Description: "Get all label names",
					Parameters: map[string]string{
						"datasource": "Datasource name",
						"start":      "Optional start time",
						"end":        "Optional end time",
					},
					Returns: "List of label names",
				},
				"get_label_values": {
					Signature:   "loki.get_label_values(datasource: str, label: str, start: str = None, end: str = None) -> list[str]",
					Description: "Get all values for a label",
					Parameters: map[string]string{
						"datasource": "Datasource name",
						"label":      "Label name",
						"start":      "Optional start time",
						"end":        "Optional end time",
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
