// Package buildoor exposes devnet buildoor builder instances to the sandbox:
// per-slot action plans, jq transforms, and slot outcome history over the
// server-side buildoor.* operations.
package buildoor

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

// Module implements the module.Module interface for the buildoor module.
type Module struct {
	cartographoorClient cartographoor.CartographoorClient
}

// New creates a new buildoor module.
func New() *Module {
	return &Module{}
}

func (m *Module) Name() string { return "buildoor" }

// DefaultEnabled implements module.DefaultEnabled.
// Buildoor is enabled by default since it requires no configuration.
func (m *Module) DefaultEnabled() bool { return true }

func (m *Module) Init(_ []byte) error { return nil }

func (m *Module) ApplyDefaults() {}

func (m *Module) Validate() error { return nil }

// SandboxEnv returns ETHPANDAOPS_BUILDOOR_NETWORKS with the network->overview
// URL mapping from cartographoor, so sandbox code can fail fast when no
// buildoor deployments exist.
func (m *Module) SandboxEnv() (map[string]string, error) {
	if m.cartographoorClient == nil {
		return nil, nil
	}

	networks := m.cartographoorClient.GetActiveNetworks()
	buildoorNetworks := make(map[string]string, len(networks))

	for name, network := range networks {
		if network.ServiceURLs != nil && network.ServiceURLs.Buildoor != "" {
			buildoorNetworks[name] = network.ServiceURLs.Buildoor
		}
	}

	if len(buildoorNetworks) == 0 {
		return nil, nil
	}

	networksJSON, err := json.Marshal(buildoorNetworks)
	if err != nil {
		return nil, fmt.Errorf("marshaling buildoor networks: %w", err)
	}

	return map[string]string{
		"ETHPANDAOPS_BUILDOOR_NETWORKS": string(networksJSON),
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
		"buildoor": {
			Description: "Drive devnet buildoor builder instances: per-slot action plans, jq transforms, slot outcomes",
			Functions: map[string]types.FunctionDoc{
				"list_networks":      {Signature: "list_networks() -> list[dict]", Description: "List networks with buildoor deployments"},
				"list_instances":     {Signature: "list_instances(network) -> list[dict]", Description: "List a network's builder instances (name, url)"},
				"get_overview":       {Signature: "get_overview(network, instance) -> dict", Description: "Instance status incl. current_slot and service states"},
				"get_action_plan":    {Signature: "get_action_plan(network, instance, min_slot, max_slot) -> dict", Description: "Per-slot action plans in the inclusive range"},
				"get_slot_results":   {Signature: "get_slot_results(network, instance, min_slot, max_slot) -> dict", Description: "Attempt-level outcome history (build, bids, reveals, inclusion, applied plan)"},
				"test_transform":     {Signature: "test_transform(network, instance, target, expression, sample_slot=None) -> dict", Description: "Evaluate a jq expression against a sample payload/bid/envelope without touching any plan"},
				"update_action_plan": {Signature: "update_action_plan(network, instance, updates, token) -> dict", Description: "Apply raw PlanUpdate mutations; token is an authenticatoor bearer token"},
				"set_transforms":     {Signature: "set_transforms(network, instance, token, slots=None, from_slot=None, to_slot=None, payload=None, bid=None, envelope=None) -> dict", Description: "Set jq transforms on future slots (>=2 ahead; '' clears one expression)"},
			},
		},
	}
}

// SetCartographoorClient implements module.CartographoorAware.
func (m *Module) SetCartographoorClient(client cartographoor.CartographoorClient) {
	m.cartographoorClient = client
}

func (m *Module) Start(_ context.Context) error { return nil }

func (m *Module) Stop(_ context.Context) error { return nil }
