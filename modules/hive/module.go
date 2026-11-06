package hive

import (
	"context"
	"maps"

	"github.com/ethpandaops/panda/pkg/hive"
	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/types"
)

// Compile-time interface checks.
var (
	_ module.Module                = (*Module)(nil)
	_ module.DefaultEnabled        = (*Module)(nil)
	_ module.SandboxEnvProvider    = (*Module)(nil)
	_ module.ExamplesProvider      = (*Module)(nil)
	_ module.PythonAPIDocsProvider = (*Module)(nil)
)

// Module implements the module.Module interface for hive test results.
type Module struct{}

// New creates a new hive module.
func New() *Module {
	return &Module{}
}

func (m *Module) Name() string { return "hive" }

// DefaultEnabled implements module.DefaultEnabled. Hive results are public,
// so the module needs no configuration.
func (m *Module) DefaultEnabled() bool { return true }

func (m *Module) Init(_ []byte) error { return nil }

func (m *Module) ApplyDefaults() {}

func (m *Module) Validate() error { return nil }

// SandboxEnv tells the sandbox where hive results come from.
func (m *Module) SandboxEnv() (map[string]string, error) {
	return map[string]string{"ETHPANDAOPS_HIVE_URL": hive.DefaultBaseURL}, nil
}

func (m *Module) Examples() map[string]types.ExampleCategory {
	result := make(map[string]types.ExampleCategory, len(queryExamples))
	maps.Copy(result, queryExamples)

	return result
}

func (m *Module) PythonAPIDocs() map[string]types.ModuleDoc {
	return map[string]types.ModuleDoc{
		"hive": {
			Description: "Query the hive test results ethPandaOps CI publishes at hive.ethpandaops.io: " +
				"which EELS tests each execution client passes and fails, with errors, logs, reproduce commands and links",
			Functions: map[string]types.FunctionDoc{
				"list_groups": {Signature: "list_groups() -> list[dict]", Description: "List result groups, such as glamsterdam-quick or heze"},
				"get_summary": {Signature: "get_summary(group) -> dict", Description: "Pass, fail and timeout counts per client and simulator, with client versions and branches"},
				"get_failures": {
					Signature:   "get_failures(group, client=None, simulator=None) -> dict",
					Description: "Failures and fail rates per client and simulator, with suite files and fixtures releases",
				},
				"list_tests": {
					Signature:   "list_tests(group, status='fail', client=None, simulator=None, filter=None, offset=0, limit=50) -> dict",
					Description: "List tests: status is fail, pass or all, filter is a case-insensitive regexp on the pytest ID. Returns total and tests, each with hive_url",
				},
				"search": {
					Signature:   "search(group, query, client=None, simulator=None, status=None, limit=50) -> dict",
					Description: "Tests whose pytest ID contains query, ignoring case",
				},
				"get_test": {
					Signature:   "get_test(group, suite_file, test_id, full_log=False) -> dict",
					Description: "One test's error, detail log, client log tail, and fill, consume and hive commands",
				},
				"find_test": {
					Signature:   "find_test(group, filter, client=None, simulator=None, full_log=False) -> dict",
					Description: "get_test for the first test whose pytest ID matches the filter regexp",
				},
				"get_diff": {
					Signature:   "get_diff(group, include_tests=False) -> dict",
					Description: "Regressions, fixes and unchanged failures between the latest two batches of runs, totals only unless include_tests",
				},
				"link_test": {Signature: "link_test(group, suite_file, test_id) -> str", Description: "Link to a test's page in the hive UI"},
			},
		},
	}
}

func (m *Module) Start(_ context.Context) error { return nil }

func (m *Module) Stop(_ context.Context) error { return nil }
