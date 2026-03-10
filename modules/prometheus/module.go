package prometheus

import (
	"context"
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/ethpandaops/mcp/pkg/module"
	"github.com/ethpandaops/mcp/pkg/types"
)

// Module implements the module.Module interface for the Prometheus module.
type Module struct {
	cfg Config
}

// New creates a new Prometheus module.
func New() *Module { return &Module{} }

func (p *Module) Name() string { return "prometheus" }

func (p *Module) Init(rawConfig []byte) error {
	if err := yaml.Unmarshal(rawConfig, &p.cfg); err != nil {
		return err
	}

	// Filter out instances with empty required fields (e.g., missing env vars).
	validInstances := make([]InstanceConfig, 0, len(p.cfg.Instances))

	for _, inst := range p.cfg.Instances {
		if inst.Name != "" && inst.URL != "" {
			validInstances = append(validInstances, inst)
		}
	}

	p.cfg.Instances = validInstances

	// If no valid instances remain, signal that this module should be skipped.
	if len(p.cfg.Instances) == 0 {
		return module.ErrNoValidConfig
	}

	return nil
}

func (p *Module) ApplyDefaults() {
	for i := range p.cfg.Instances {
		if p.cfg.Instances[i].Timeout == 0 {
			p.cfg.Instances[i].Timeout = 60
		}
	}
}

func (p *Module) Validate() error {
	names := make(map[string]struct{}, len(p.cfg.Instances))
	for i, inst := range p.cfg.Instances {
		if inst.Name == "" {
			return fmt.Errorf("instances[%d].name is required", i)
		}
		if _, exists := names[inst.Name]; exists {
			return fmt.Errorf("instances[%d].name %q is duplicated", i, inst.Name)
		}
		names[inst.Name] = struct{}{}
		if inst.URL == "" {
			return fmt.Errorf("instances[%d].url is required", i)
		}
	}
	return nil
}

// SandboxEnv returns credential-free environment variables for the sandbox.
// Credentials are never passed to sandbox containers - they connect via
// the credential proxy instead.
func (p *Module) SandboxEnv() (map[string]string, error) {
	if len(p.cfg.Instances) == 0 {
		return nil, nil
	}

	// Return datasource info without credentials.
	type datasourceInfo struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}

	infos := make([]datasourceInfo, 0, len(p.cfg.Instances))
	for _, inst := range p.cfg.Instances {
		infos = append(infos, datasourceInfo{
			Name:        inst.Name,
			Description: inst.Description,
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

func (p *Module) DatasourceInfo() []types.DatasourceInfo {
	infos := make([]types.DatasourceInfo, 0, len(p.cfg.Instances))
	for _, inst := range p.cfg.Instances {
		infos = append(infos, types.DatasourceInfo{
			Type:        "prometheus",
			Name:        inst.Name,
			Description: inst.Description,
			Metadata: map[string]string{
				"url": inst.URL,
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

func (p *Module) Start(_ context.Context) error { return nil }

func (p *Module) Stop(_ context.Context) error { return nil }
