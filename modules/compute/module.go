// Package compute integrates a control-plane for ephemeral compute sandboxes
// (Firecracker microVMs). Datasources come from proxy discovery: the proxy
// holds the compute API URL and credentials, and the module exposes operations
// over sandboxes, snapshots, templates, async operations, SSH keys, and the
// service directory.
package compute

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

// Module implements the module.Module interface for compute.
type Module struct {
	dsMu        sync.RWMutex
	datasources []types.DatasourceInfo
}

// New creates a new compute module.
func New() *Module { return &Module{} }

func (m *Module) Name() string { return "compute" }

// InitFromDiscovery initializes the module from discovered datasources.
// Safe to call repeatedly: subsequent calls replace the datasource list in
// place so the proxy client's periodic refresh propagates without a restart.
//
// Always writes the filtered list, including when empty — see the comment on
// the clickhouse module's InitFromDiscovery for the rationale.
func (m *Module) InitFromDiscovery(datasources []types.DatasourceInfo) error {
	filtered := make([]types.DatasourceInfo, 0, len(datasources))

	for _, ds := range datasources {
		if ds.Type != "compute" {
			continue
		}

		filtered = append(filtered, ds)
	}

	m.dsMu.Lock()
	m.datasources = filtered
	m.dsMu.Unlock()

	if len(filtered) == 0 {
		return module.ErrNoValidConfig
	}

	return nil
}

// Init is a no-op: the module's datasources come from proxy discovery via
// InitFromDiscovery.
func (m *Module) Init(_ []byte) error { return nil }

// ApplyDefaults sets default values before validation.
func (m *Module) ApplyDefaults() {}

// Validate checks that the parsed config is valid.
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

// SandboxEnv returns environment variables for the sandbox.
func (m *Module) SandboxEnv() (map[string]string, error) {
	m.dsMu.RLock()
	defer m.dsMu.RUnlock()

	if len(m.datasources) == 0 {
		return nil, nil
	}

	type datasourceInfo struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}

	infos := make([]datasourceInfo, 0, len(m.datasources))
	for _, ds := range m.datasources {
		infos = append(infos, datasourceInfo{
			Name:        ds.Name,
			Description: ds.Description,
		})
	}

	infosJSON, err := json.Marshal(infos)
	if err != nil {
		return nil, fmt.Errorf("marshaling compute datasource info: %w", err)
	}

	return map[string]string{
		"ETHPANDAOPS_COMPUTE_DATASOURCES": string(infosJSON),
	}, nil
}

// DatasourceInfo returns datasource metadata for datasources:// resources.
func (m *Module) DatasourceInfo() []types.DatasourceInfo {
	m.dsMu.RLock()
	defer m.dsMu.RUnlock()

	result := make([]types.DatasourceInfo, len(m.datasources))
	copy(result, m.datasources)

	return result
}

// Examples returns query examples for the compute module.
func (m *Module) Examples() map[string]types.ExampleCategory {
	result := make(map[string]types.ExampleCategory, len(queryExamples))
	maps.Copy(result, queryExamples)

	return result
}

// PythonAPIDocs returns the compute module documentation.
func (m *Module) PythonAPIDocs() map[string]types.ModuleDoc {
	return map[string]types.ModuleDoc{
		"compute": {
			Description: "Control-plane for ephemeral compute sandboxes (Firecracker microVMs): create sandboxes from templates, snapshot/stop/start/lease them, restore snapshots, and poll async operations",
			Functions: map[string]types.FunctionDoc{
				"list_datasources":         {Signature: "list_datasources() -> list[dict]", Description: "List available compute datasources"},
				"list_sandboxes":           {Signature: "list_sandboxes(datasource=None, limit=100, offset=0, cursor=None) -> dict", Description: "List sandboxes (paginated)"},
				"get_sandbox":              {Signature: "get_sandbox(sandbox_id, datasource=None) -> dict", Description: "Get one sandbox by id"},
				"create_sandbox":           {Signature: "create_sandbox(template, ttl=None, on_delete=None, idempotency_key=None, datasource=None) -> dict", Description: "Create a sandbox from a template; returns a 202 operation to poll. on_delete is one of archive|cold|delete|hot"},
				"delete_sandbox":           {Signature: "delete_sandbox(sandbox_id, idempotency_key=None, datasource=None) -> dict", Description: "Delete a sandbox; returns an operation to poll"},
				"stop_sandbox":             {Signature: "stop_sandbox(sandbox_id, idempotency_key=None, datasource=None) -> dict", Description: "Stop a running sandbox; returns an operation to poll"},
				"start_sandbox":            {Signature: "start_sandbox(sandbox_id, idempotency_key=None, datasource=None) -> dict", Description: "Start a stopped sandbox; returns an operation to poll"},
				"snapshot_sandbox":         {Signature: "snapshot_sandbox(sandbox_id, note=None, idempotency_key=None, datasource=None) -> dict", Description: "Snapshot a sandbox; returns an operation to poll"},
				"lease_sandbox":            {Signature: "lease_sandbox(sandbox_id, extend, datasource=None) -> dict", Description: "Extend a sandbox's TTL; extend is a Go-duration string (e.g. '1h', '30m')"},
				"prepare_sandbox_ssh":      {Signature: "prepare_sandbox_ssh(sandbox_id, public_key, datasource=None) -> dict", Description: "Mint a short-lived SSH gateway certificate for a registered public key; returns host, port, username, and client_certificate"},
				"get_sandbox_snapshots":    {Signature: "get_sandbox_snapshots(sandbox_id, datasource=None) -> dict", Description: "List snapshots taken from a sandbox"},
				"get_sandbox_operations":   {Signature: "get_sandbox_operations(sandbox_id, datasource=None) -> dict", Description: "List async operations for a sandbox"},
				"get_sandbox_logs":         {Signature: "get_sandbox_logs(sandbox_id, datasource=None) -> dict", Description: "Fetch a sandbox's logs"},
				"get_sandbox_lineage":      {Signature: "get_sandbox_lineage(sandbox_id, datasource=None) -> dict", Description: "Get a sandbox's lineage (snapshot/restore ancestry)"},
				"list_snapshots":           {Signature: "list_snapshots(datasource=None, limit=100, offset=0, cursor=None) -> dict", Description: "List snapshots (paginated)"},
				"get_snapshot":             {Signature: "get_snapshot(snapshot_id, datasource=None) -> dict", Description: "Get one snapshot by id"},
				"delete_snapshot":          {Signature: "delete_snapshot(snapshot_id, idempotency_key=None, datasource=None) -> dict", Description: "Delete a snapshot; returns an operation to poll"},
				"restore_snapshot":         {Signature: "restore_snapshot(snapshot_id, ttl=None, idempotency_key=None, datasource=None) -> dict", Description: "Restore a snapshot into a new sandbox; returns an operation to poll"},
				"get_snapshot_restored_by": {Signature: "get_snapshot_restored_by(snapshot_id, datasource=None) -> dict", Description: "List sandboxes restored from a snapshot"},
				"list_templates":           {Signature: "list_templates(datasource=None, limit=100, offset=0, cursor=None) -> dict", Description: "List sandbox templates (paginated)"},
				"get_template":             {Signature: "get_template(name, version, datasource=None) -> dict", Description: "Get one template by name and version"},
				"list_operations":          {Signature: "list_operations(datasource=None, limit=100, offset=0, cursor=None) -> dict", Description: "List async operations (paginated)"},
				"get_operation":            {Signature: "get_operation(operation_id, datasource=None) -> dict", Description: "Poll one async operation by id (returned by mutations)"},
				"list_ssh_keys":            {Signature: "list_ssh_keys(datasource=None, limit=100, offset=0, cursor=None) -> dict", Description: "List the caller's SSH public keys"},
				"add_ssh_key":              {Signature: "add_ssh_key(public_key, name=None, datasource=None) -> dict", Description: "Register an SSH public key for the caller"},
				"delete_ssh_key":           {Signature: "delete_ssh_key(key_id, datasource=None) -> dict", Description: "Delete one of the caller's SSH public keys"},
				"list_users":               {Signature: "list_users(datasource=None) -> dict", Description: "List directory users"},
				"get_user":                 {Signature: "get_user(handle, datasource=None) -> dict", Description: "Get one directory user by handle"},
				"list_nodes":               {Signature: "list_nodes(datasource=None) -> dict", Description: "List compute nodes"},
				"get_node":                 {Signature: "get_node(node_id, datasource=None) -> dict", Description: "Get one compute node by id"},
				"list_audit":               {Signature: "list_audit(datasource=None) -> dict", Description: "List audit-log entries"},
				"meta":                     {Signature: "meta(datasource=None) -> dict", Description: "Get service metadata (version, limits, capabilities)"},
			},
		},
	}
}

// Start performs async initialization.
func (m *Module) Start(_ context.Context) error { return nil }

// Stop cleans up resources.
func (m *Module) Stop(_ context.Context) error { return nil }
