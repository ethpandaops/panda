package forky

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

// Module implements the module.Module interface for the Forky module.
type Module struct {
	cartographoorClient cartographoor.CartographoorClient
}

// New creates a new Forky module.
func New() *Module {
	return &Module{}
}

func (m *Module) Name() string { return "forky" }

// DefaultEnabled implements module.DefaultEnabled.
// Forky is enabled by default since it requires no configuration.
func (m *Module) DefaultEnabled() bool { return true }

func (m *Module) Init(_ []byte) error { return nil }

func (m *Module) ApplyDefaults() {}

func (m *Module) Validate() error {
	return nil
}

// SandboxEnv returns environment variables for the sandbox.
// Returns ETHPANDAOPS_FORKY_NETWORKS with network->URL mapping from cartographoor.
func (m *Module) SandboxEnv() (map[string]string, error) {
	if m.cartographoorClient == nil {
		return nil, nil
	}

	// Build network -> Forky URL mapping from cartographoor data.
	networks := m.cartographoorClient.GetActiveNetworks()
	forkyNetworks := make(map[string]string, len(networks))

	for name, network := range networks {
		if network.ServiceURLs != nil && network.ServiceURLs.Forky != "" {
			forkyNetworks[name] = network.ServiceURLs.Forky
		}
	}

	if len(forkyNetworks) == 0 {
		return nil, nil
	}

	networksJSON, err := json.Marshal(forkyNetworks)
	if err != nil {
		return nil, fmt.Errorf("marshaling forky networks: %w", err)
	}

	return map[string]string{
		"ETHPANDAOPS_FORKY_NETWORKS": string(networksJSON),
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
		"forky": {
			Description: "Query Forky fork-choice snapshots (frames) captured from beacon nodes, and generate deep links",
			Functions: map[string]types.FunctionDoc{
				"list_networks": {Signature: "list_networks() -> list[dict]", Description: "List networks with Forky instances"},
				"get_base_url":  {Signature: "get_base_url(network) -> str", Description: "Get Forky base URL for a network"},
				"get_now":       {Signature: "get_now(network) -> dict", Description: "Get the network's current wall-clock slot and epoch"},
				"get_spec":      {Signature: "get_spec(network) -> dict", Description: "Get the network name and chain spec (seconds_per_slot, slots_per_epoch, genesis_time)"},
				"list_frames":   {Signature: "list_frames(network, node=None, slot=None, epoch=None, labels=None, consensus_client=None, event_source=None, before=None, after=None, offset=0, limit=100) -> dict", Description: "List fork-choice frame metadata matching a filter; returns {frames, total}"},
				"get_frame":     {Signature: "get_frame(network, frame_id) -> dict", Description: "Get a full fork-choice frame (metadata + fork_choice_nodes dump) by ID"},
				"list_nodes":    {Signature: "list_nodes(network, slot=None, epoch=None, ..., offset=0, limit=100) -> dict", Description: "List distinct node names with frames matching a filter; returns {nodes, total}"},
				"list_slots":    {Signature: "list_slots(network, node=None, epoch=None, ..., offset=0, limit=100) -> dict", Description: "List distinct wall-clock slots with frames matching a filter; returns {slots, total}"},
				"list_epochs":   {Signature: "list_epochs(network, node=None, slot=None, ..., offset=0, limit=100) -> dict", Description: "List distinct wall-clock epochs with frames matching a filter; returns {epochs, total}"},
				"list_labels":   {Signature: "list_labels(network, node=None, slot=None, ..., offset=0, limit=100) -> dict", Description: "List distinct frame labels matching a filter; returns {labels, total}"},
				"link_frame":    {Signature: "link_frame(network, frame_id) -> str", Description: "Deep link to a frame snapshot in the Forky UI"},
				"link_node":     {Signature: "link_node(network, node) -> str", Description: "Deep link to a node's live view in the Forky UI"},
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
