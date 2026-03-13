package cbt

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"

	"gopkg.in/yaml.v3"

	"github.com/ethpandaops/panda/pkg/cartographoor"
	"github.com/ethpandaops/panda/pkg/types"
)

// Module implements the module.Module interface for the CBT module.
type Module struct {
	cfg                 Config
	cartographoorClient cartographoor.CartographoorClient
}

// New creates a new CBT module.
func New() *Module {
	return &Module{}
}

func (p *Module) Name() string { return "cbt" }

// Enabled reports whether CBT operations should be exposed.
func (p *Module) Enabled() bool { return p.cfg.IsEnabled() }

// DefaultEnabled implements module.DefaultEnabled.
// CBT is enabled by default since it requires no configuration.
func (p *Module) DefaultEnabled() bool { return true }

func (p *Module) Init(rawConfig []byte) error {
	if len(rawConfig) == 0 {
		// No config provided, use defaults (enabled = true).
		return nil
	}

	return yaml.Unmarshal(rawConfig, &p.cfg)
}

func (p *Module) ApplyDefaults() {
	// Defaults are handled by Config.IsEnabled().
}

func (p *Module) Validate() error {
	// No validation needed - config is minimal.
	return nil
}

// SandboxEnv returns environment variables for the sandbox.
// Returns ETHPANDAOPS_CBT_NETWORKS with network->URL mapping derived from
// cartographoor active networks using the convention https://cbt.{network}.ethpandaops.io.
func (p *Module) SandboxEnv() (map[string]string, error) {
	if !p.cfg.IsEnabled() {
		return nil, nil
	}

	if p.cartographoorClient == nil {
		// Cartographoor client not yet set - return empty.
		// This will be populated after SetCartographoorClient is called.
		return nil, nil
	}

	// Build network -> CBT URL mapping from cartographoor data.
	networks := p.cartographoorClient.GetActiveNetworks()
	cbtNetworks := make(map[string]string, len(networks))

	for name := range networks {
		cbtNetworks[name] = fmt.Sprintf("https://cbt.%s.ethpandaops.io", name)
	}

	if len(cbtNetworks) == 0 {
		return nil, nil
	}

	networksJSON, err := json.Marshal(cbtNetworks)
	if err != nil {
		return nil, fmt.Errorf("marshaling cbt networks: %w", err)
	}

	return map[string]string{
		"ETHPANDAOPS_CBT_NETWORKS": string(networksJSON),
	}, nil
}

// DatasourceInfo returns empty since networks are the datasources,
// and those come from cartographoor.
func (p *Module) DatasourceInfo() []types.DatasourceInfo {
	return nil
}

func (p *Module) Examples() map[string]types.ExampleCategory {
	if !p.cfg.IsEnabled() {
		return nil
	}

	result := make(map[string]types.ExampleCategory, len(queryExamples))
	maps.Copy(result, queryExamples)

	return result
}

func (p *Module) PythonAPIDocs() map[string]types.ModuleDoc {
	if !p.cfg.IsEnabled() {
		return nil
	}

	return map[string]types.ModuleDoc{
		"cbt": {
			Description: "Query CBT (ClickHouse Build Tool) for data model metadata, transformation status, and coverage",
			Functions: map[string]types.FunctionDoc{
				"list_networks":               {Signature: "list_networks() -> list[dict]", Description: "List networks with CBT instances"},
				"list_models":                 {Signature: "list_models(network, type=None, database=None, search=None) -> list[dict]", Description: "List all data models"},
				"list_external_models":        {Signature: "list_external_models(network, database=None) -> list[dict]", Description: "List external ClickHouse models"},
				"get_external_model":          {Signature: "get_external_model(network, id) -> dict", Description: "Get external model by ID (database.table)"},
				"get_external_bounds":         {Signature: "get_external_bounds(network, id=None) -> list|dict", Description: "Get data bounds for external models"},
				"list_transformations":        {Signature: "list_transformations(network, database=None, type=None, status=None) -> list[dict]", Description: "List data transformations"},
				"get_transformation":          {Signature: "get_transformation(network, id) -> dict", Description: "Get transformation details"},
				"get_transformation_coverage": {Signature: "get_transformation_coverage(network, id=None) -> list|dict", Description: "Get transformation coverage"},
				"get_scheduled_runs":          {Signature: "get_scheduled_runs(network, id=None) -> list|dict", Description: "Get scheduled transformation runs"},
				"get_interval_types":          {Signature: "get_interval_types(network) -> dict", Description: "Get interval type configurations"},
				"link_model":                  {Signature: "link_model(network, id) -> str", Description: "Deep link to model in CBT UI"},
			},
		},
	}
}

func (p *Module) GettingStartedSnippet() string {
	if !p.cfg.IsEnabled() {
		return ""
	}

	return `## CBT (ClickHouse Build Tool)

Query CBT for data model metadata, transformation status, coverage, and bounds.
Generate deep links to view models in the CBT web UI.

` + "```python" + `
from ethpandaops import cbt

# List networks with CBT instances
networks = cbt.list_networks()

# List all models for a network
models = cbt.list_models("mainnet")
print(f"Total models: {len(models)}")

# Check transformation coverage
coverage = cbt.get_transformation_coverage("mainnet")
for c in coverage:
    print(f"  {c.get('id')}: {c}")

# Generate a deep link to a model
link = cbt.link_model("mainnet", "default.beacon_api_eth_v1_events_block")
print(f"View in CBT: {link}")
` + "```" + `
`
}

// SetCartographoorClient implements module.CartographoorAware.
// This is called by the builder to inject the cartographoor client.
func (p *Module) SetCartographoorClient(client cartographoor.CartographoorClient) {
	p.cartographoorClient = client
}

func (p *Module) Start(_ context.Context) error { return nil }

func (p *Module) Stop(_ context.Context) error { return nil }
