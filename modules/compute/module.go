// Package compute integrates a control-plane for ephemeral compute sandboxes
// (Firecracker microVMs). Datasources come from proxy discovery: the proxy
// holds the compute API URL and credentials, and the module exposes operations
// over sandboxes, images, async operations, SSH keys, and the service
// directory.
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
			Description: "Control-plane for ephemeral compute sandboxes (Firecracker microVMs): create sandboxes from images (a named template or a raw snapshot), snapshot/stop/start/lease them, fan out copies with fork, and poll async operations",
			Functions: map[string]types.FunctionDoc{
				"list_datasources":       {Signature: "list_datasources() -> list[dict]", Description: "List available compute datasources"},
				"list_sandboxes":         {Signature: "list_sandboxes(datasource=None, limit=100, offset=0, cursor=None) -> dict", Description: "List sandboxes (paginated)"},
				"get_sandbox":            {Signature: "get_sandbox(sandbox_id, datasource=None) -> dict", Description: "Get one sandbox by id"},
				"create_sandbox":         {Signature: "create_sandbox(template=None, ttl=None, on_delete=None, idempotency_key=None, datasource=None, snapshot_id=None, flavor=None, name=None, vcpu=None, memory_mb=None, disk_gb=None, env=None, labels=None, hooks=None, watchdog=None, paused=None) -> dict", Description: "Create a sandbox from a template or snapshot; returns a 202 operation to poll. flavor for snapshot sources is warm (resume memory, default) or cold (fresh boot on the snapshot disk with free vcpu/memory). paused=True leaves a warm snapshot boot paused. on_delete is one of archive|delete|hot"},
				"delete_sandbox":         {Signature: "delete_sandbox(sandbox_id, idempotency_key=None, datasource=None) -> dict", Description: "Delete a sandbox; returns an operation to poll"},
				"stop_sandbox":           {Signature: "stop_sandbox(sandbox_id, idempotency_key=None, datasource=None) -> dict", Description: "Stop a running sandbox; returns an operation to poll"},
				"start_sandbox":          {Signature: "start_sandbox(sandbox_id, idempotency_key=None, datasource=None) -> dict", Description: "Start a stopped sandbox; returns an operation to poll"},
				"snapshot_sandbox":       {Signature: "snapshot_sandbox(sandbox_id, note=None, ttl=None, idempotency_key=None, datasource=None) -> dict", Description: "Snapshot a sandbox; returns an operation to poll"},
				"lease_sandbox":          {Signature: "lease_sandbox(sandbox_id, extend, datasource=None) -> dict", Description: "Extend a sandbox's TTL; extend is a Go-duration string (e.g. '1h', '30m')"},
				"prepare_sandbox_ssh":    {Signature: "prepare_sandbox_ssh(sandbox_id, public_key, datasource=None) -> dict", Description: "Mint a short-lived SSH gateway certificate for a registered public key; returns host, port, username, and client_certificate"},
				"get_sandbox_images":     {Signature: "get_sandbox_images(sandbox_id, datasource=None) -> dict", Description: "List raw images captured from a sandbox"},
				"expose_port":            {Signature: "expose_port(sandbox_id, port, name=None, protocol=None, service=None, managed=None, idempotency_key=None, datasource=None) -> dict", Description: "Expose a sandbox port through the ingress gateway"},
				"unexpose_port":          {Signature: "unexpose_port(sandbox_id, port, idempotency_key=None, datasource=None) -> dict", Description: "Remove an exposed sandbox port"},
				"get_sandbox_operations": {Signature: "get_sandbox_operations(sandbox_id, datasource=None) -> dict", Description: "List async operations for a sandbox"},
				"get_sandbox_logs":       {Signature: "get_sandbox_logs(sandbox_id, datasource=None, source=None, tail_bytes=None) -> dict", Description: "Fetch a sandbox's logs; source is console or firecracker"},
				"exec_sandbox":           {Signature: "exec_sandbox(sandbox_id, command, timeout=None, datasource=None) -> dict", Description: "Run an argv command inside a sandbox (no shell); returns exit_code, stdout, stderr"},
				"get_sandbox_metrics":    {Signature: "get_sandbox_metrics(sandbox_id, datasource=None) -> dict", Description: "Fetch guest resource metrics for a sandbox"},
				"pause_sandbox":          {Signature: "pause_sandbox(sandbox_id, idempotency_key=None, datasource=None) -> dict", Description: "Pause a running sandbox's vCPUs; returns an operation to poll"},
				"resume_sandbox":         {Signature: "resume_sandbox(sandbox_id, idempotency_key=None, datasource=None) -> dict", Description: "Resume a paused sandbox; returns an operation to poll"},
				"get_sandbox_hooks":      {Signature: "get_sandbox_hooks(sandbox_id, datasource=None) -> dict", Description: "List lifecycle hooks declared on a sandbox"},
				"get_sandbox_hook_runs":  {Signature: "get_sandbox_hook_runs(sandbox_id, datasource=None) -> dict", Description: "List lifecycle hook executions for a sandbox"},
				"get_sandbox_lineage":    {Signature: "get_sandbox_lineage(sandbox_id, datasource=None) -> dict", Description: "Get a sandbox's lineage (snapshot/restore ancestry)"},
				"list_images":            {Signature: "list_images(datasource=None, limit=100, offset=0, cursor=None) -> dict", Description: "List images (paginated): named images first, then raw snapshots newest-first. kind is named or raw; flavor is warm or cold"},
				"get_image":              {Signature: "get_image(image_id, datasource=None) -> dict", Description: "Get one image by snapshot id (raw) or name@version (named)"},
				"delete_image":           {Signature: "delete_image(image_id, idempotency_key=None, datasource=None) -> dict", Description: "Delete a raw image; returns an operation to poll. Named images are deactivated instead (409)"},
				"promote_image":          {Signature: "promote_image(image_id, name, version=None, replace=None, description=None, tags=None, idempotency_key=None, datasource=None) -> dict", Description: "Promote a raw image into a named warm image"},
				"deactivate_image":       {Signature: "deactivate_image(image_id, idempotency_key=None, datasource=None) -> dict", Description: "Retire a named image; accepts name or name@version"},
				"fork_image":             {Signature: "fork_image(image_id, count, ttl=None, min_ready=None, deadline=None, flavor=None, paused=None, idempotency_key=None, datasource=None) -> dict", Description: "Fan out count sandboxes from an image; raw images fork directly, named warm images fork their backing snapshot. Returns fork_id and op_id to poll. For a single sandbox use create_sandbox(snapshot_id=...)"},
				"fork_sandbox":           {Signature: "fork_sandbox(sandbox_id, count, ttl=None, min_ready=None, deadline=None, flavor=None, paused=None, idempotency_key=None, datasource=None) -> dict", Description: "Capture a running sandbox as an ephemeral snapshot and fan out count copies; the source keeps running. Returns fork_id and op_id to poll"},
				"list_forks":             {Signature: "list_forks(datasource=None) -> list[dict]", Description: "List fork operations and their progress counts"},
				"get_fork":               {Signature: "get_fork(fork_id, datasource=None) -> dict", Description: "Get one fork operation by id, including per-child state"},
				"get_image_restored_by":  {Signature: "get_image_restored_by(image_id, datasource=None) -> dict", Description: "List sandboxes created from an image"},
				"get_image_lineage":      {Signature: "get_image_lineage(image_id, datasource=None) -> dict", Description: "Show the full lineage tree rooted at an image"},
				"list_bakes":             {Signature: "list_bakes(datasource=None, limit=100, offset=0, cursor=None) -> dict", Description: "List scheduled image bakes and their status"},
				"run_bake":               {Signature: "run_bake(name, idempotency_key=None, datasource=None) -> dict", Description: "Trigger a bake outside its schedule; returns an operation to poll"},
				"list_operations":        {Signature: "list_operations(datasource=None, limit=100, offset=0, cursor=None) -> dict", Description: "List async operations (paginated)"},
				"get_operation":          {Signature: "get_operation(operation_id, datasource=None) -> dict", Description: "Poll one async operation by id (returned by mutations)"},
				"list_ssh_keys":          {Signature: "list_ssh_keys(datasource=None, limit=100, offset=0, cursor=None) -> dict", Description: "List the caller's SSH public keys"},
				"add_ssh_key":            {Signature: "add_ssh_key(public_key, name=None, datasource=None) -> dict", Description: "Register an SSH public key for the caller"},
				"delete_ssh_key":         {Signature: "delete_ssh_key(key_id, datasource=None) -> dict", Description: "Delete one of the caller's SSH public keys"},
				"list_users":             {Signature: "list_users(datasource=None) -> dict", Description: "List directory users"},
				"get_user":               {Signature: "get_user(handle, datasource=None) -> dict", Description: "Get one directory user by handle"},
				"list_nodes":             {Signature: "list_nodes(datasource=None) -> dict", Description: "List compute nodes"},
				"get_node":               {Signature: "get_node(node_id, datasource=None) -> dict", Description: "Get one compute node by id"},
				"list_audit":             {Signature: "list_audit(datasource=None) -> dict", Description: "List audit-log entries"},
				"meta":                   {Signature: "meta(datasource=None) -> dict", Description: "Get service metadata (version, limits, capabilities)"},
				"list_api_operations":    {Signature: "list_api_operations(datasource=None) -> dict", Description: "List the operations the compute service currently advertises, with their path/query/body arguments"},
				"call":                   {Signature: "call(operation, datasource=None, **kwargs) -> dict", Description: "Call any compute API operation by name; the interface is discovered from the running service, so operations added upstream work without a panda upgrade"},
			},
		},
	}
}

// Start performs async initialization.
func (m *Module) Start(_ context.Context) error { return nil }

// Stop cleans up resources.
func (m *Module) Stop(_ context.Context) error { return nil }
