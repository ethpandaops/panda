// Package benchmarkoor integrates the benchmarkoor execution-client
// benchmarking service. Datasources come from proxy discovery: the proxy
// holds the benchmarkoor API URL and read-only API key, and the module
// exposes read operations over indexed benchmark runs, suites, and per-test
// performance stats.
package benchmarkoor

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

// Module implements the module.Module interface for benchmarkoor.
type Module struct {
	dsMu        sync.RWMutex
	datasources []types.DatasourceInfo
}

// New creates a new benchmarkoor module.
func New() *Module { return &Module{} }

func (m *Module) Name() string { return "benchmarkoor" }

// InitFromDiscovery initializes the module from discovered datasources.
// Safe to call repeatedly: subsequent calls replace the datasource list in
// place so the proxy client's periodic refresh propagates without a restart.
//
// Always writes the filtered list, including when empty — see the comment on
// the clickhouse module's InitFromDiscovery for the rationale.
func (m *Module) InitFromDiscovery(datasources []types.DatasourceInfo) error {
	filtered := make([]types.DatasourceInfo, 0, len(datasources))

	for _, ds := range datasources {
		if ds.Type != "benchmarkoor" {
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
		return nil, fmt.Errorf("marshaling benchmarkoor datasource info: %w", err)
	}

	return map[string]string{
		"ETHPANDAOPS_BENCHMARKOOR_DATASOURCES": string(infosJSON),
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

// Examples returns query examples for the benchmarkoor module.
func (m *Module) Examples() map[string]types.ExampleCategory {
	result := make(map[string]types.ExampleCategory, len(queryExamples))
	maps.Copy(result, queryExamples)

	return result
}

// PythonAPIDocs returns the benchmarkoor module documentation.
func (m *Module) PythonAPIDocs() map[string]types.ModuleDoc {
	return map[string]types.ModuleDoc{
		"benchmarkoor": {
			Description: "Query benchmarkoor for execution-client benchmark results: runs, suites, per-test gas throughput (MGas/s), and per-block EL timing",
			Functions: map[string]types.FunctionDoc{
				"list_datasources": {Signature: "list_datasources() -> list[dict]", Description: "List available benchmarkoor datasources"},
				"list_runs":        {Signature: "list_runs(datasource=None, client=None, status=None, suite_hash=None, filters=None, order=None, select=None, limit=100, offset=0) -> list[dict]", Description: "Query indexed benchmark runs (PostgREST-style filters, e.g. {'tests_failed': 'gt.0'})"},
				"get_run":          {Signature: "get_run(run_id, datasource=None) -> dict", Description: "Get one indexed run by run_id, including per-step stats (steps_json)"},
				"list_suites":      {Signature: "list_suites(datasource=None, filters=None, order=None, limit=100, offset=0) -> list[dict]", Description: "List indexed test suites (suite_hash, name, tests_total)"},
				"get_suite_stats":  {Signature: "get_suite_stats(suite_hash, datasource=None, max_runs_per_client=None) -> dict", Description: "Per-test duration/gas history for a suite, keyed by test name, across recent runs per client"},
				"query_test_stats": {Signature: "query_test_stats(datasource=None, run_id=None, client=None, test_name=None, suite_hash=None, filters=None, order=None, select=None, limit=100, offset=0) -> list[dict]", Description: "Query per-test stats rows (gas, time, MGas/s, CPU/memory/disk resource deltas)"},
				"query_block_logs": {Signature: "query_block_logs(datasource=None, run_id=None, client=None, test_name=None, filters=None, order=None, select=None, limit=100, offset=0) -> list[dict]", Description: "Query per-block EL timing rows (execution/state-read/hash/commit ms, throughput, state and cache counters)"},
				"list_live_runs":   {Signature: "list_live_runs(datasource=None) -> list[dict]", Description: "List currently-running benchmark runs with live progress"},
				"get_index":        {Signature: "get_index(datasource=None) -> dict", Description: "Full run index (all runs with instance metadata and step summaries)"},
				"get_file":         {Signature: "get_file(path, datasource=None) -> dict | list | bytes", Description: "Fetch a stored result file (e.g. '<discovery_path>/runs/<run_id>/result.json'); JSON is parsed, other content returns bytes, S3-backed instances return {'url': presigned}"},
				"link_run":         {Signature: "link_run(run_id, datasource=None) -> str", Description: "Deep link to a run in the benchmarkoor web UI"},
				"link_suite":       {Signature: "link_suite(suite_hash, datasource=None) -> str", Description: "Deep link to a suite in the benchmarkoor web UI"},
			},
		},
	}
}

// Start performs async initialization.
func (m *Module) Start(_ context.Context) error { return nil }

// Stop cleans up resources.
func (m *Module) Stop(_ context.Context) error { return nil }
