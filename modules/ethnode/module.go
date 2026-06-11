package ethnode

import (
	"context"
	"maps"
	"sync"

	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/types"
)

// Compile-time interface checks.
var (
	_ module.Module                 = (*Module)(nil)
	_ module.ProxyDiscoverable      = (*Module)(nil)
	_ module.SandboxEnvProvider     = (*Module)(nil)
	_ module.DatasourceInfoProvider = (*Module)(nil)
	_ module.ExamplesProvider       = (*Module)(nil)
	_ module.PythonAPIDocsProvider  = (*Module)(nil)
)

// Module implements the module.Module interface for direct Ethereum node API access.
type Module struct {
	dsMu        sync.RWMutex
	datasources []types.DatasourceInfo
}

// New creates a new ethnode module.
func New() *Module {
	return &Module{}
}

func (m *Module) Name() string { return "ethnode" }

// InitFromDiscovery enables the module if an ethnode datasource exists.
// Safe to call repeatedly: it replaces the stored list so the proxy client's
// periodic refresh propagates without a restart.
func (m *Module) InitFromDiscovery(datasources []types.DatasourceInfo) error {
	filtered := make([]types.DatasourceInfo, 0, len(datasources))

	for _, ds := range datasources {
		if ds.Type == "ethnode" {
			filtered = append(filtered, ds)
		}
	}

	m.dsMu.Lock()
	m.datasources = filtered
	m.dsMu.Unlock()

	if len(filtered) == 0 {
		return module.ErrNoValidConfig
	}

	return nil
}

func (m *Module) Init(_ []byte) error { return nil }

func (m *Module) ApplyDefaults() {}

func (m *Module) Validate() error { return nil }

// SandboxEnv returns environment variables for the sandbox.
func (m *Module) SandboxEnv() (map[string]string, error) {
	return map[string]string{
		"ETHPANDAOPS_ETHNODE_AVAILABLE": "true",
	}, nil
}

// DatasourceInfo returns the discovered ethnode datasource. Ethnode is a single
// type-level entry rather than a named list: the proxy relays to any
// {network}/{instance} host on demand and holds no enumerable instance list.
func (m *Module) DatasourceInfo() []types.DatasourceInfo {
	m.dsMu.RLock()
	defer m.dsMu.RUnlock()

	result := make([]types.DatasourceInfo, len(m.datasources))
	copy(result, m.datasources)

	return result
}

// Examples returns query examples for ethnode.
func (m *Module) Examples() map[string]types.ExampleCategory {
	result := make(map[string]types.ExampleCategory, len(queryExamples))
	maps.Copy(result, queryExamples)

	return result
}

// PythonAPIDocs returns API documentation for the ethnode Python module.
func (m *Module) PythonAPIDocs() map[string]types.ModuleDoc {
	return map[string]types.ModuleDoc{
		"ethnode": {
			Description: "Direct access to Ethereum beacon and execution node APIs",
			Functions: map[string]types.FunctionDoc{
				// Discovery functions.
				"list_datasources": {Signature: "list_datasources() -> list[dict]", Description: "List available ethnode datasources"},
				"list_networks":    {Signature: "list_networks() -> list[dict]", Description: "List active network ids reachable for direct node access"},
				// Beacon node (CL) functions.
				"get_node_version":         {Signature: "get_node_version(network, instance) -> dict", Description: "Get beacon node software version"},
				"get_node_syncing":         {Signature: "get_node_syncing(network, instance) -> dict", Description: "Get beacon node sync status"},
				"get_node_health":          {Signature: "get_node_health(network, instance) -> int", Description: "Get beacon node health status code"},
				"get_peers":                {Signature: "get_peers(network, instance) -> dict", Description: "Get connected peers list"},
				"get_peer_count":           {Signature: "get_peer_count(network, instance) -> dict", Description: "Get peer count summary"},
				"get_beacon_headers":       {Signature: "get_beacon_headers(network, instance, slot='head') -> dict", Description: "Get beacon block header"},
				"get_finality_checkpoints": {Signature: "get_finality_checkpoints(network, instance, state_id='head') -> dict", Description: "Get finality checkpoints"},
				"get_config_spec":          {Signature: "get_config_spec(network, instance) -> dict", Description: "Get chain config spec"},
				"get_fork_schedule":        {Signature: "get_fork_schedule(network, instance) -> dict", Description: "Get fork schedule"},
				"get_deposit_contract":     {Signature: "get_deposit_contract(network, instance) -> dict", Description: "Get deposit contract info"},
				// Execution node (EL) functions.
				"eth_block_number":        {Signature: "eth_block_number(network, instance) -> int", Description: "Get latest block number"},
				"eth_syncing":             {Signature: "eth_syncing(network, instance) -> dict | bool", Description: "Get EL sync status"},
				"eth_chain_id":            {Signature: "eth_chain_id(network, instance) -> int", Description: "Get chain ID"},
				"eth_get_block_by_number": {Signature: "eth_get_block_by_number(network, instance, block='latest', full_tx=False) -> dict", Description: "Get block by number"},
				"net_peer_count":          {Signature: "net_peer_count(network, instance) -> int", Description: "Get EL peer count"},
				"web3_client_version":     {Signature: "web3_client_version(network, instance) -> str", Description: "Get EL client version"},
				// Generic pass-through.
				"beacon_get":    {Signature: "beacon_get(network, instance, path, params=None) -> dict", Description: "GET any beacon API endpoint and return the raw JSON payload"},
				"beacon_post":   {Signature: "beacon_post(network, instance, path, body=None) -> dict", Description: "POST any beacon API endpoint and return the raw JSON payload"},
				"execution_rpc": {Signature: "execution_rpc(network, instance, method, params=None) -> any", Description: "Call any JSON-RPC method and return the raw result"},
			},
		},
	}
}

func (m *Module) Start(_ context.Context) error { return nil }

func (m *Module) Stop(_ context.Context) error { return nil }
