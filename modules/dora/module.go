package dora

import (
	"context"
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/ethpandaops/panda/pkg/cartographoor"
	"github.com/ethpandaops/panda/pkg/types"
)

// Module implements the module.Module interface for the Dora module.
type Module struct {
	cfg                 Config
	cartographoorClient cartographoor.CartographoorClient
}

// New creates a new Dora module.
func New() *Module {
	return &Module{}
}

func (p *Module) Name() string { return "dora" }

// Enabled reports whether Dora operations should be exposed.
func (p *Module) Enabled() bool { return p.cfg.IsEnabled() }

// DefaultEnabled implements module.DefaultEnabled.
// Dora is enabled by default since it requires no configuration.
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
// Returns ETHPANDAOPS_DORA_NETWORKS with network->URL mapping from cartographoor.
func (p *Module) SandboxEnv() (map[string]string, error) {
	if !p.cfg.IsEnabled() {
		return nil, nil
	}

	if p.cartographoorClient == nil {
		// Cartographoor client not yet set - return empty.
		// This will be populated after SetCartographoorClient is called.
		return nil, nil
	}

	// Build network -> Dora URL mapping from cartographoor data.
	networks := p.cartographoorClient.GetActiveNetworks()
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
func (p *Module) DatasourceInfo() []types.DatasourceInfo {
	return nil
}

func (p *Module) Examples() map[string]types.ExampleCategory {
	if !p.cfg.IsEnabled() {
		return nil
	}

	result := make(map[string]types.ExampleCategory, len(queryExamples))
	for k, v := range queryExamples {
		result[k] = v
	}

	return result
}

func (p *Module) PythonAPIDocs() map[string]types.ModuleDoc {
	if !p.cfg.IsEnabled() {
		return nil
	}

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

func (p *Module) GettingStartedSnippet() string {
	if !p.cfg.IsEnabled() {
		return ""
	}

	return `## Dora Beacon Chain Explorer

Query the Dora beacon chain explorer for network status, validators, and slots.
Generate deep links to view data in the Dora web UI.

` + "```python" + `
from ethpandaops import dora

# List networks with Dora explorers
networks = dora.list_networks()

# Get network overview
overview = dora.get_network_overview("hoodi")
print(f"Current epoch: {overview['current_epoch']}")

# Look up a validator and get a deep link
validator = dora.get_validator("hoodi", "12345")
link = dora.link_validator("hoodi", "12345")
print(f"View in Dora: {link}")
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
