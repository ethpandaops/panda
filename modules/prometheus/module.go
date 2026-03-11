package prometheus

import (
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/types"
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
	examples    map[string]types.ExampleCategory
}

// New creates a new Prometheus module.
func New() *Module { return &Module{} }

func (ext *Module) Name() string { return "prometheus" }

// InitFromDiscovery initializes the module from discovered datasources.
func (ext *Module) InitFromDiscovery(datasources []types.DatasourceInfo) error {
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

// Examples returns query examples for the Prometheus module.
func (ext *Module) Examples() map[string]types.ExampleCategory {
	return module.CloneExampleCatalog(ext.examples)
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

// PythonAPIDocs returns the Prometheus module documentation.
func (ext *Module) PythonAPIDocs() map[string]types.ModuleDoc {
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
