package tracoor

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

// Module implements the module.Module interface for the Tracoor module.
type Module struct {
	cartographoorClient cartographoor.CartographoorClient
}

// New creates a new Tracoor module.
func New() *Module {
	return &Module{}
}

func (m *Module) Name() string { return "tracoor" }

// DefaultEnabled implements module.DefaultEnabled.
// Tracoor is enabled by default since it requires no configuration.
func (m *Module) DefaultEnabled() bool { return true }

func (m *Module) Init(_ []byte) error { return nil }

func (m *Module) ApplyDefaults() {}

func (m *Module) Validate() error {
	return nil
}

// SandboxEnv returns environment variables for the sandbox.
// Returns ETHPANDAOPS_TRACOOR_NETWORKS with network->URL mapping from cartographoor.
func (m *Module) SandboxEnv() (map[string]string, error) {
	if m.cartographoorClient == nil {
		return nil, nil
	}

	// Build network -> Tracoor URL mapping from cartographoor data.
	networks := m.cartographoorClient.GetActiveNetworks()
	tracoorNetworks := make(map[string]string, len(networks))

	for name, network := range networks {
		if network.ServiceURLs != nil && network.ServiceURLs.Tracoor != "" {
			tracoorNetworks[name] = network.ServiceURLs.Tracoor
		}
	}

	if len(tracoorNetworks) == 0 {
		return nil, nil
	}

	networksJSON, err := json.Marshal(tracoorNetworks)
	if err != nil {
		return nil, fmt.Errorf("marshaling tracoor networks: %w", err)
	}

	return map[string]string{
		"ETHPANDAOPS_TRACOOR_NETWORKS": string(networksJSON),
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
		"tracoor": {
			Description: "Query Tracoor forensics archives: beacon states/blocks, invalid (bad) blocks and blobs rejected by nodes, and execution debug traces, with raw artifact download URLs",
			Functions: map[string]types.FunctionDoc{
				"list_networks":              {Signature: "list_networks() -> list[dict]", Description: "List networks with Tracoor instances"},
				"get_base_url":               {Signature: "get_base_url(network) -> str", Description: "Get Tracoor base URL for a network"},
				"get_config":                 {Signature: "get_config(network) -> dict", Description: "Get the instance's Ethereum network-config and tool settings"},
				"list_beacon_states":         {Signature: "list_beacon_states(network, node=None, slot=None, epoch=None, state_root=None, node_version=None, beacon_implementation=None, before=None, after=None, id=None, offset=0, limit=100, order_by=None) -> list[dict]", Description: "List captured beacon states (SSZ snapshots); each entry has id, node, fetched_at, slot, epoch, state_root, beacon_implementation"},
				"count_beacon_states":        {Signature: "count_beacon_states(network, **filters) -> int", Description: "Count captured beacon states matching a filter"},
				"list_beacon_blocks":         {Signature: "list_beacon_blocks(network, node=None, slot=None, epoch=None, block_root=None, ..., offset=0, limit=100) -> list[dict]", Description: "List captured beacon blocks"},
				"count_beacon_blocks":        {Signature: "count_beacon_blocks(network, **filters) -> int", Description: "Count captured beacon blocks matching a filter"},
				"list_beacon_bad_blocks":     {Signature: "list_beacon_bad_blocks(network, node=None, slot=None, epoch=None, block_root=None, ..., offset=0, limit=100) -> list[dict]", Description: "List invalid beacon blocks that nodes rejected from gossip — prime forensics material"},
				"count_beacon_bad_blocks":    {Signature: "count_beacon_bad_blocks(network, **filters) -> int", Description: "Count invalid beacon blocks matching a filter"},
				"list_beacon_bad_blobs":      {Signature: "list_beacon_bad_blobs(network, node=None, slot=None, epoch=None, block_root=None, index=None, ..., offset=0, limit=100) -> list[dict]", Description: "List invalid blob sidecars that nodes rejected from gossip"},
				"count_beacon_bad_blobs":     {Signature: "count_beacon_bad_blobs(network, **filters) -> int", Description: "Count invalid blob sidecars matching a filter"},
				"list_execution_traces":      {Signature: "list_execution_traces(network, node=None, block_number=None, block_hash=None, execution_implementation=None, ..., offset=0, limit=100) -> list[dict]", Description: "List execution-layer debug_traceBlock captures"},
				"count_execution_traces":     {Signature: "count_execution_traces(network, **filters) -> int", Description: "Count execution trace captures matching a filter"},
				"list_execution_bad_blocks":  {Signature: "list_execution_bad_blocks(network, node=None, block_number=None, block_hash=None, block_extra_data=None, ..., offset=0, limit=100) -> list[dict]", Description: "List invalid execution blocks (debug_getBadBlocks captures)"},
				"count_execution_bad_blocks": {Signature: "count_execution_bad_blocks(network, **filters) -> int", Description: "Count invalid execution blocks matching a filter"},
				"list_unique_values":         {Signature: "list_unique_values(network, artifact, fields) -> dict", Description: "Distinct values for fields of an artifact type (e.g. which nodes/implementations have captures); artifact is one of beacon_state, beacon_block, beacon_bad_block, beacon_bad_blob, execution_block_trace, execution_bad_block"},
				"get_download_url":           {Signature: "get_download_url(network, artifact, id) -> str", Description: "URL serving the raw stored artifact (SSZ or JSON, possibly gzip) for a capture ID"},
				"link":                       {Signature: "link(network, artifact, id=None) -> str", Description: "Deep link to an artifact listing or a single capture in the Tracoor UI"},
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
