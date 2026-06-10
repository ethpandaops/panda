package cbt

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"

	"github.com/ethpandaops/panda/pkg/cartographoor"
	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/types"
)

// Compile-time interface checks.
var (
	_ module.Module                 = (*Module)(nil)
	_ module.DefaultEnabled         = (*Module)(nil)
	_ module.CartographoorAware     = (*Module)(nil)
	_ module.SandboxEnvProvider     = (*Module)(nil)
	_ module.DatasourceInfoProvider = (*Module)(nil)
	_ module.ExamplesProvider       = (*Module)(nil)
	_ module.PythonAPIDocsProvider  = (*Module)(nil)
)

// Module implements the module.Module interface for the CBT module.
type Module struct {
	cartographoorClient cartographoor.CartographoorClient
}

// New creates a new CBT module.
func New() *Module {
	return &Module{}
}

// NetworkBaseURL returns the CBT instance base URL for a network, derived from
// the standard ethpandaops.io naming convention. Cartographoor discovery does
// not expose a CBT service URL, so the per-network host is derived here.
func NetworkBaseURL(network string) string {
	return fmt.Sprintf("https://cbt.%s.ethpandaops.io", network)
}

func (m *Module) Name() string { return "cbt" }

// DefaultEnabled implements module.DefaultEnabled.
// CBT is enabled by default since it requires no configuration.
func (m *Module) DefaultEnabled() bool { return true }

func (m *Module) Init(_ []byte) error { return nil }

func (m *Module) ApplyDefaults() {}

func (m *Module) Validate() error {
	return nil
}

// SandboxEnv returns environment variables for the sandbox.
// Returns ETHPANDAOPS_CBT_NETWORKS with network->URL mapping derived from
// cartographoor active networks using the convention https://cbt.{network}.ethpandaops.io.
func (m *Module) SandboxEnv() (map[string]string, error) {
	if m.cartographoorClient == nil {
		return nil, nil
	}

	// Build network -> CBT URL mapping from cartographoor data.
	networks := m.cartographoorClient.GetActiveNetworks()
	cbtNetworks := make(map[string]string, len(networks))

	for name := range networks {
		cbtNetworks[name] = NetworkBaseURL(name)
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
func (m *Module) DatasourceInfo() []types.DatasourceInfo {
	return nil
}

func (m *Module) Examples() map[string]types.ExampleCategory {
	result := make(map[string]types.ExampleCategory, len(queryExamples))
	maps.Copy(result, queryExamples)

	return result
}

func (m *Module) PythonAPIDocs() map[string]types.ModuleDoc {
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

// SetCartographoorClient implements module.CartographoorAware.
// This is called by the builder to inject the cartographoor client.
func (m *Module) SetCartographoorClient(client cartographoor.CartographoorClient) {
	m.cartographoorClient = client
}

func (m *Module) Start(_ context.Context) error { return nil }

func (m *Module) Stop(_ context.Context) error { return nil }
