package server

import (
	_ "embed"
	"net/http"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// benchmarkoorSpecYAML is a vendored copy of benchmarkoor's embedded OpenAPI
// spec (pkg/api/openapi.yaml at ethpandaops/benchmarkoor@9b8a5d8c, 2026-06-05).
// Benchmarkoor does not export the spec from its Go module, so the copy is
// pinned here; the e2e harness (scripts/e2e-benchmarkoor.sh) verifies it
// still matches the running instance's /api/v1/openapi.json.
//
//go:embed testdata/benchmarkoor_openapi.yaml
var benchmarkoorSpecYAML []byte

// benchmarkoorSpecRoutes lists every benchmarkoor API route the
// benchmarkoor.* operations invoke — spec-style templates without the /api/v1
// prefix — with the query parameters panda forwards on each. The runtime path
// stays loosely typed on purpose (drift tolerance across benchmarkoor
// versions); these tests catch panda drifting from the spec when the vendored
// copy is refreshed.
//
// Intentionally not asserted against the spec (upstream spec gaps, exercised
// by the e2e harness instead):
//   - GET /index/live_runs (benchmarkoor.list_live_runs)
//   - GET /index/query/suites (benchmarkoor.list_suites)
//   - the max_runs_per_client query param on /index/suites/{hash}/stats
//   - the filter columns on /index/query/test_stats_block_logs (the spec
//     description documents none, though the endpoint filters like the others)
var benchmarkoorSpecRoutes = []struct {
	operation string
	path      string
	params    []string
}{
	{"benchmarkoor.get_index", "/index/", nil},
	{"benchmarkoor.list_runs", "/index/query/runs", []string{"limit", "offset", "select", "order"}},
	{"benchmarkoor.get_run", "/index/query/runs", []string{"limit"}},
	{"benchmarkoor.get_suite_stats", "/index/suites/{hash}/stats", nil},
	{"benchmarkoor.query_test_stats", "/index/query/test_stats", []string{"limit", "offset", "select", "order"}},
	{"benchmarkoor.query_block_logs", "/index/query/test_stats_block_logs", []string{"limit", "offset", "select", "order"}},
	{"benchmarkoor.get_file", "/files/{path}", nil},
}

func loadBenchmarkoorSpec(t *testing.T) *openapi3.T {
	t.Helper()

	loader := openapi3.NewLoader()
	spec, err := loader.LoadFromData(benchmarkoorSpecYAML)
	require.NoError(t, err)

	return spec
}

func TestBenchmarkoorRoutesMatchSpec(t *testing.T) {
	t.Parallel()

	spec := loadBenchmarkoorSpec(t)

	for _, route := range benchmarkoorSpecRoutes {
		t.Run(route.operation+" "+route.path, func(t *testing.T) {
			t.Parallel()

			item := spec.Paths.Find(route.path)
			require.NotNil(t, item, "path %s missing from the benchmarkoor OpenAPI spec", route.path)

			operation := item.GetOperation(http.MethodGet)
			require.NotNil(t, operation, "path %s has no GET operation in the benchmarkoor OpenAPI spec", route.path)

			declared := make(map[string]bool, len(operation.Parameters))
			for _, ref := range operation.Parameters {
				if ref.Value != nil && ref.Value.In == "query" {
					declared[ref.Value.Name] = true
				}
			}

			for _, param := range route.params {
				assert.True(t, declared[param],
					"query param %q is not declared on GET %s in the benchmarkoor OpenAPI spec", param, route.path)
			}
		})
	}
}

// TestBenchmarkoorEqShorthandColumnsInSpec pins the equality-shorthand args
// (client=eq.X etc.) to the filter columns the spec documents per endpoint.
// The columns are listed in the description text rather than as declared
// parameters, so the assertion checks the documented column lists.
func TestBenchmarkoorEqShorthandColumnsInSpec(t *testing.T) {
	t.Parallel()

	spec := loadBenchmarkoorSpec(t)

	routes := map[string][]string{
		"/index/query/runs":       {"run_id", "client", "status", "suite_hash"},
		"/index/query/test_stats": {"run_id", "client", "test_name", "suite_hash"},
	}

	for path, columns := range routes {
		item := spec.Paths.Find(path)
		require.NotNil(t, item, "path %s missing from the benchmarkoor OpenAPI spec", path)

		operation := item.GetOperation(http.MethodGet)
		require.NotNil(t, operation)

		for _, column := range columns {
			assert.Contains(t, operation.Description, column,
				"filter column %q is not documented on GET %s", column, path)
		}
	}
}

// TestBenchmarkoorSpecServesAPIV1 anchors the /api/v1 prefix panda hardcodes
// in front of every spec path.
func TestBenchmarkoorSpecServesAPIV1(t *testing.T) {
	t.Parallel()

	spec := loadBenchmarkoorSpec(t)

	require.NotEmpty(t, spec.Servers)

	for _, server := range spec.Servers {
		assert.True(t, strings.HasSuffix(server.URL, "/api/v1"),
			"benchmarkoor spec server %q is not rooted at /api/v1", server.URL)
	}
}
