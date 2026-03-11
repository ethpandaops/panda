package ethnode

import (
	"context"

	"gopkg.in/yaml.v3"

	"github.com/ethpandaops/mcp/pkg/types"
)

// Module implements the module.Module interface for direct Ethereum node API access.
type Module struct {
	cfg Config
}

// New creates a new ethnode module.
func New() *Module {
	return &Module{}
}

func (p *Module) Name() string { return "ethnode" }

// Enabled reports whether ethnode operations should be exposed.
func (p *Module) Enabled() bool { return p.cfg.IsEnabled() }

func (p *Module) Init(rawConfig []byte) error {
	if len(rawConfig) == 0 {
		return nil
	}

	return yaml.Unmarshal(rawConfig, &p.cfg)
}

func (p *Module) ApplyDefaults() {}

func (p *Module) Validate() error { return nil }

// SandboxEnv returns environment variables for the sandbox.
// Only a credential-free availability signal is sent; actual requests go through the proxy.
func (p *Module) SandboxEnv() (map[string]string, error) {
	if !p.cfg.IsEnabled() {
		return nil, nil
	}

	return map[string]string{
		"ETHPANDAOPS_ETHNODE_AVAILABLE": "true",
	}, nil
}

// DatasourceInfo returns empty since ethnode is a pass-through proxy, not a named datasource.
func (p *Module) DatasourceInfo() []types.DatasourceInfo {
	return nil
}

// Examples returns query examples for ethnode.
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

// PythonAPIDocs returns API documentation for the ethnode Python module.
func (p *Module) PythonAPIDocs() map[string]types.ModuleDoc {
	if !p.cfg.IsEnabled() {
		return nil
	}

	return map[string]types.ModuleDoc{
		"ethnode": {
			Description: "Direct access to Ethereum beacon and execution node APIs",
			Functions: map[string]types.FunctionDoc{
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

// GettingStartedSnippet returns a Markdown snippet for the getting-started resource.
func (p *Module) GettingStartedSnippet() string {
	if !p.cfg.IsEnabled() {
		return ""
	}

	return `## Ethereum Node API (Direct Access)

Query individual beacon and execution nodes directly. Useful for checking sync status,
peer counts, finality checkpoints, and comparing state across nodes during devnet debugging.

Node instances follow the naming convention: ` + "`{client_cl}-{client_el}-{index}`" + ` (e.g., "lighthouse-geth-1").

` + "```python" + `
from ethpandaops import ethnode

# Check beacon node sync status
syncing = ethnode.get_node_syncing("my-devnet", "lighthouse-geth-1")
print(f"Head slot: {syncing['data']['head_slot']}")

# Check EL block number
block_num = ethnode.eth_block_number("my-devnet", "lighthouse-geth-1")
print(f"Latest block: {block_num}")

# Check finality
checkpoints = ethnode.get_finality_checkpoints("my-devnet", "lighthouse-geth-1")
print(f"Finalized epoch: {checkpoints['data']['finalized']['epoch']}")

# Generic beacon API call
identity = ethnode.beacon_get("my-devnet", "lighthouse-geth-1", "/eth/v1/node/identity")
` + "```" + `
`
}

func (p *Module) Start(_ context.Context) error { return nil }

func (p *Module) Stop(_ context.Context) error { return nil }
