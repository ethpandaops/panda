package loki

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"

	"github.com/ethpandaops/mcp/pkg/extension"
	"github.com/ethpandaops/mcp/pkg/types"
)

type Extension struct {
	cfg Config
}

func New() *Extension { return &Extension{} }

func (p *Extension) Name() string { return "loki" }

func (p *Extension) Init(rawConfig []byte) error {
	if err := yaml.Unmarshal(rawConfig, &p.cfg); err != nil {
		return err
	}

	// Drop unnamed instances; remaining fields are optional when proxy is authoritative.
	validInstances := make([]InstanceConfig, 0, len(p.cfg.Instances))

	for _, inst := range p.cfg.Instances {
		if inst.Name != "" {
			validInstances = append(validInstances, inst)
		}
	}

	p.cfg.Instances = validInstances

	// If no named instances remain, signal that this extension should be skipped.
	if len(p.cfg.Instances) == 0 {
		return extension.ErrNoValidConfig
	}

	return nil
}

func (p *Extension) ApplyDefaults() {
	for i := range p.cfg.Instances {
		if p.cfg.Instances[i].Timeout == 0 {
			p.cfg.Instances[i].Timeout = 60
		}
	}
}

func (p *Extension) Validate() error {
	names := make(map[string]struct{}, len(p.cfg.Instances))
	for i, inst := range p.cfg.Instances {
		if inst.Name == "" {
			return fmt.Errorf("instances[%d].name is required", i)
		}
		if _, exists := names[inst.Name]; exists {
			return fmt.Errorf("instances[%d].name %q is duplicated", i, inst.Name)
		}
		names[inst.Name] = struct{}{}
	}
	return nil
}

// SandboxEnv returns credential-free environment variables for the sandbox.
// Credentials are never passed to sandbox containers - they connect via
// the credential proxy instead.
func (p *Extension) SandboxEnv() (map[string]string, error) {
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
		return nil, fmt.Errorf("marshaling Loki datasource info: %w", err)
	}

	return map[string]string{
		"ETHPANDAOPS_LOKI_DATASOURCES": string(infosJSON),
	}, nil
}

func (p *Extension) DatasourceInfo() []types.DatasourceInfo {
	infos := make([]types.DatasourceInfo, 0, len(p.cfg.Instances))
	for _, inst := range p.cfg.Instances {
		infos = append(infos, types.DatasourceInfo{
			Type:        "loki",
			Name:        inst.Name,
			Description: inst.Description,
			Metadata: map[string]string{
				"url": inst.URL,
			},
		})
	}
	return infos
}

func (p *Extension) Examples() map[string]types.ExampleCategory {
	result := make(map[string]types.ExampleCategory, len(queryExamples))
	for k, v := range queryExamples {
		result[k] = v
	}
	return result
}

// PythonAPIDocs with full Loki module docs
func (p *Extension) PythonAPIDocs() map[string]types.ModuleDoc {
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

func (p *Extension) GettingStartedSnippet() string { return "" }
func (p *Extension) RegisterResources(_ logrus.FieldLogger, _ extension.ResourceRegistry) error {
	return nil
}
func (p *Extension) Start(_ context.Context) error { return nil }
func (p *Extension) Stop(_ context.Context) error  { return nil }
