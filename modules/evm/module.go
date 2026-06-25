package evm

import (
	"context"
	"encoding/json"
	"maps"
	"sync"

	"github.com/ethpandaops/panda/pkg/cartographoor"
	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/types"
)

// Compile-time interface checks.
var (
	_ module.Module                = (*Module)(nil)
	_ module.ProxyDiscoverable     = (*Module)(nil)
	_ module.CartographoorAware    = (*Module)(nil)
	_ module.SandboxEnvProvider    = (*Module)(nil)
	_ module.ExamplesProvider      = (*Module)(nil)
	_ module.PythonAPIDocsProvider = (*Module)(nil)
)

// Module exposes EVM execution utilities to the Python sandbox.
// It activates when an ethnode datasource is available and adds evm.call,
// evm.trace, evm.trace_tx, evm.tx, evm.assemble, evm.disassemble,
// evm.wallet, and evm.faucet to the sandbox environment.
type Module struct {
	dsMu        sync.RWMutex
	datasources []types.DatasourceInfo

	cartographoorClient cartographoor.CartographoorClient
}

// New creates a new evm module.
func New() *Module { return &Module{} }

func (m *Module) Name() string { return "evm" }

// InitFromDiscovery activates the module when ethnode datasources are present.
// The evm module delegates all RPC execution to ethnode — it never holds its
// own proxy credentials.
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

// SetCartographoorClient implements module.CartographoorAware.
func (m *Module) SetCartographoorClient(client cartographoor.CartographoorClient) {
	m.cartographoorClient = client
}

func (m *Module) Init(_ []byte) error { return nil }
func (m *Module) ApplyDefaults()      {}
func (m *Module) Validate() error     { return nil }

func (m *Module) Start(_ context.Context) error { return nil }
func (m *Module) Stop(_ context.Context) error  { return nil }

// SandboxEnv injects EVM availability and per-network faucet URLs.
func (m *Module) SandboxEnv() (map[string]string, error) {
	env := map[string]string{"ETHPANDAOPS_EVM_AVAILABLE": "true"}

	if m.cartographoorClient == nil {
		return env, nil
	}

	faucets := make(map[string]string)

	for name, net := range m.cartographoorClient.GetActiveNetworks() {
		if net.ServiceURLs != nil && net.ServiceURLs.Faucet != "" {
			faucets[name] = net.ServiceURLs.Faucet
		}
	}

	if len(faucets) > 0 {
		data, err := json.Marshal(faucets)
		if err == nil {
			env["ETHPANDAOPS_EVM_FAUCET_NETWORKS"] = string(data)
		}
	}

	return env, nil
}

// Examples returns query examples for the evm module.
func (m *Module) Examples() map[string]types.ExampleCategory {
	result := make(map[string]types.ExampleCategory, len(queryExamples))
	maps.Copy(result, queryExamples)

	return result
}

// PythonAPIDocs returns API documentation for the evm Python module.
func (m *Module) PythonAPIDocs() map[string]types.ModuleDoc {
	return map[string]types.ModuleDoc{
		"evm": {
			Description: "EVM execution, tracing, transaction submission, and bytecode assembly against devnet nodes",
			Functions: map[string]types.FunctionDoc{
				"call": {
					Signature:   "call(network, instance, data, to=None, from_=None, value=0, gas=None, block='latest') -> str",
					Description: "Execute bytecode or calldata; returns raw hex result. to=None runs data as init code (creation context).",
				},
				"trace": {
					Signature:   "trace(network, instance, data, to=None, from_=None, gas=None, block='latest') -> list[dict]",
					Description: "Trace execution opcode-by-opcode via debug_traceCall. Returns [{op, pc, gas, gas_cost, stack, memory, depth}]. to=None traces init code.",
				},
				"trace_tx": {
					Signature:   "trace_tx(network, instance, txhash) -> list[dict]",
					Description: "Trace an already-mined transaction via debug_traceTransaction. Same structLog format as trace(). Use for post-deployment triage of fuzz contracts.",
				},
				"tx": {
					Signature:   "tx(network, instance, private_key, to, data='0x', value=0, gas=None) -> str",
					Description: "Sign and submit an EIP-1559 transaction; returns tx hash. to=None deploys a contract (data is init code). gas=None estimates automatically.",
				},
				"assemble": {
					Signature:   "assemble(ops: list) -> str",
					Description: "Assemble a list of opcode names and immediate ints into hex bytecode. Example: ['PUSH1', 0x01, 'PUSH1', 0x01, 'ADD', 'STOP']",
				},
				"disassemble": {
					Signature:   "disassemble(bytecode: str) -> list[dict]",
					Description: "Disassemble hex bytecode into [{pc, op, operand}] dicts.",
				},
				"wallet": {
					Signature:   "wallet(private_key=None) -> dict",
					Description: "Generate a new keypair or derive address from an existing private key. Returns {address, private_key}.",
				},
				"faucet": {
					Signature:   "faucet(network, address) -> str",
					Description: "Return the PoW faucet URL for the network pre-filled with the given address. Open in a browser to request test ETH.",
				},
			},
		},
	}
}
