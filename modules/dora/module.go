package dora

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

// Module implements the module.Module interface for the Dora module.
type Module struct {
	cartographoorClient cartographoor.CartographoorClient
}

// New creates a new Dora module.
func New() *Module {
	return &Module{}
}

func (m *Module) Name() string { return "dora" }

// DefaultEnabled implements module.DefaultEnabled.
// Dora is enabled by default since it requires no configuration.
func (m *Module) DefaultEnabled() bool { return true }

func (m *Module) Init(_ []byte) error { return nil }

func (m *Module) ApplyDefaults() {}

func (m *Module) Validate() error {
	return nil
}

// SandboxEnv returns environment variables for the sandbox.
// Returns ETHPANDAOPS_DORA_NETWORKS with network->URL mapping from cartographoor.
func (m *Module) SandboxEnv() (map[string]string, error) {
	if m.cartographoorClient == nil {
		return nil, nil
	}

	// Build network -> Dora URL mapping from cartographoor data.
	networks := m.cartographoorClient.GetActiveNetworks()
	doraNetworks := make(map[string]string, len(networks))

	for name, network := range networks {
		if network.ServiceURLs != nil && network.ServiceURLs.Dora != "" {
			doraNetworks[name] = network.ServiceURLs.Dora
		}
	}

	if len(doraNetworks) == 0 {
		return nil, nil
	}

	networksJSON, err := json.Marshal(doraNetworks)
	if err != nil {
		return nil, fmt.Errorf("marshaling dora networks: %w", err)
	}

	return map[string]string{
		"ETHPANDAOPS_DORA_NETWORKS": string(networksJSON),
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
		"dora": {
			Description: "Query Dora beacon chain explorer and generate deep links",
			Functions: map[string]types.FunctionDoc{
				"list_networks":        {Signature: "list_networks() -> list[dict]", Description: "List networks with Dora explorers"},
				"get_base_url":         {Signature: "get_base_url(network) -> str", Description: "Get Dora base URL for a network"},
				"get_network_overview": {Signature: "get_network_overview(network) -> dict", Description: "Get epoch, slot, validator counts"},
				"get_validator":        {Signature: "get_validator(network, index_or_pubkey) -> dict", Description: "Get validator by index or pubkey"},
				"get_validators":       {Signature: "get_validators(network, status=None, limit=100) -> list", Description: "List validators with optional filter"},
				"get_slot":             {Signature: "get_slot(network, slot_or_hash) -> dict", Description: "Get slot by number or hash"},
				"get_epoch":            {Signature: "get_epoch(network, epoch) -> dict", Description: "Get epoch summary"},
				"link_validator":       {Signature: "link_validator(network, index_or_pubkey) -> str", Description: "Deep link to validator"},
				"link_slot":            {Signature: "link_slot(network, slot_or_hash) -> str", Description: "Deep link to slot"},
				"link_epoch":           {Signature: "link_epoch(network, epoch) -> str", Description: "Deep link to epoch"},
				"link_address":         {Signature: "link_address(network, address) -> str", Description: "Deep link to address"},
				"link_block":           {Signature: "link_block(network, number_or_hash) -> str", Description: "Deep link to block"},
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
