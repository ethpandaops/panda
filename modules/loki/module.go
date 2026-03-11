package loki

import (
	"fmt"
	"maps"

	"gopkg.in/yaml.v3"

	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/types"
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
	examples    map[string]types.ExampleCategory
}

// New creates a new Loki module.
func New() *Module { return &Module{} }

func (ext *Module) Name() string { return "loki" }

// InitFromDiscovery initializes the module from discovered datasources.
func (ext *Module) InitFromDiscovery(datasources []types.DatasourceInfo) error {
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

	ext.datasources = filtered

	return nil
}

// Init parses the raw YAML config for this module.
func (ext *Module) Init(rawConfig []byte) error {
	if err := yaml.Unmarshal(rawConfig, &ext.cfg); err != nil {
		return err
	}

	// Drop unnamed instances.
	validInstances := make([]InstanceConfig, 0, len(ext.cfg.Instances))
	for _, inst := range ext.cfg.Instances {
		if inst.Name != "" {
			validInstances = append(validInstances, inst)
		}
	}

	ext.cfg.Instances = validInstances

	if len(ext.cfg.Instances) == 0 {
		return module.ErrNoValidConfig
	}

	// Populate internal datasources from config.
	ext.datasources = make([]types.DatasourceInfo, 0, len(ext.cfg.Instances))
	for _, inst := range ext.cfg.Instances {
		ext.datasources = append(ext.datasources, types.DatasourceInfo{
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
// Validate checks that the parsed config is valid.
func (ext *Module) Validate() error {
	if err := ext.ensureExamplesLoaded(); err != nil {
		return err
	}

	names := make(map[string]struct{}, len(ext.datasources))
	for i, ds := range ext.datasources {
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

// Examples returns query examples for the Loki module.
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

// PythonAPIDocs returns the Loki module documentation.
func (ext *Module) PythonAPIDocs() map[string]types.ModuleDoc {
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
