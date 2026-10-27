// Package devnet relays API calls to devnet services (spamoor, assertoor)
// through the proxy, which holds the credential that authorizes writes. The
// module is active when the proxy advertises at least one devnet service to
// the caller; network-to-service resolution comes from cartographoor.
package devnet

import (
	"context"
	"encoding/json"
	"fmt"
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

// Module implements the module.Module interface for the devnet relay.
type Module struct {
	dsMu        sync.RWMutex
	datasources []types.DatasourceInfo
}

// New creates a new devnet module.
func New() *Module { return &Module{} }

func (m *Module) Name() string { return "devnet" }

// InitFromDiscovery keeps the devnet services the proxy advertises. Safe to
// call repeatedly; an empty list deactivates the module.
func (m *Module) InitFromDiscovery(datasources []types.DatasourceInfo) error {
	filtered := make([]types.DatasourceInfo, 0, len(datasources))

	for _, ds := range datasources {
		if ds.Type == "devnet" {
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

// Init is a no-op: services come from proxy discovery.
func (m *Module) Init(_ []byte) error { return nil }

// ApplyDefaults sets default values before validation.
func (m *Module) ApplyDefaults() {}

// Validate checks that the discovered services are uniquely named.
func (m *Module) Validate() error {
	m.dsMu.RLock()
	defer m.dsMu.RUnlock()

	names := make(map[string]struct{}, len(m.datasources))
	for i, ds := range m.datasources {
		if ds.Name == "" {
			return fmt.Errorf("datasource[%d].name is required", i)
		}

		if _, exists := names[ds.Name]; exists {
			return fmt.Errorf("datasource[%d].name %q is duplicated", i, ds.Name)
		}

		names[ds.Name] = struct{}{}
	}

	return nil
}

// SandboxEnv exposes the relayed service names to sandbox code.
func (m *Module) SandboxEnv() (map[string]string, error) {
	m.dsMu.RLock()
	defer m.dsMu.RUnlock()

	if len(m.datasources) == 0 {
		return nil, nil
	}

	names := make([]string, 0, len(m.datasources))
	for _, ds := range m.datasources {
		names = append(names, ds.Name)
	}

	namesJSON, err := json.Marshal(names)
	if err != nil {
		return nil, fmt.Errorf("marshaling devnet services: %w", err)
	}

	return map[string]string{"ETHPANDAOPS_DEVNET_SERVICES": string(namesJSON)}, nil
}

// DatasourceInfo returns datasource metadata for datasources:// resources.
func (m *Module) DatasourceInfo() []types.DatasourceInfo {
	m.dsMu.RLock()
	defer m.dsMu.RUnlock()

	result := make([]types.DatasourceInfo, len(m.datasources))
	copy(result, m.datasources)

	return result
}

// Examples returns query examples for the devnet module.
func (m *Module) Examples() map[string]types.ExampleCategory {
	result := make(map[string]types.ExampleCategory, len(queryExamples))
	maps.Copy(result, queryExamples)

	return result
}

// PythonAPIDocs returns the devnet module documentation.
func (m *Module) PythonAPIDocs() map[string]types.ModuleDoc {
	return map[string]types.ModuleDoc{
		"devnet": {
			Description: "Call spamoor (transaction spammers) and assertoor (test runner) APIs on a devnet through the proxy, " +
				"which authorizes writes. Paths are the service's own /api/... routes; " +
				"spamoor: GET /api/spammers, /api/scenarios, /api/spammer/{id}, /api/spammer/{id}/logs, /api/clients; " +
				"POST /api/spammer {name, description, scenario, config (YAML string), startImmediately}, " +
				"/api/spammer/{id}/start, /api/spammer/{id}/pause; PUT /api/spammer/{id} {name, description, config}; " +
				"DELETE /api/spammer/{id}. assertoor: GET /api/v1/tests, /api/v1/test_runs, /api/v1/test_run/{runId}, " +
				"/api/v1/test_run/{runId}/status, /api/v1/test_run/{runId}/details; " +
				"POST /api/v1/test_runs/schedule {test_id, config, allow_duplicate}, /api/v1/test_run/{runId}/cancel. " +
				"Streaming (/stream, /events) endpoints are not supported.",
			Functions: map[string]types.FunctionDoc{
				"list_services": {Signature: "list_services() -> list[str]", Description: "Devnet services the proxy relays for you (e.g. ['assertoor', 'spamoor'])"},
				"list_networks": {Signature: "list_networks() -> list[dict]", Description: "Active networks with spamoor/assertoor: each {name, services: {service: {url, relayed}}}"},
				"request":       {Signature: "request(network, service, method, path, json=None, params=None) -> Any", Description: "Call a service API path (e.g. 'POST', '/api/spammer/12/start'); JSON responses are parsed, others return text. Raises on non-2xx"},
			},
		},
	}
}

// Start performs async initialization.
func (m *Module) Start(_ context.Context) error { return nil }

// Stop cleans up resources.
func (m *Module) Stop(_ context.Context) error { return nil }
