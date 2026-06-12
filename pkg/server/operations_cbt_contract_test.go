package server

import (
	"net/http"
	"strings"
	"testing"

	cbtapi "github.com/ethpandaops/cbt/pkg/api/generated"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cbtSpecRoutes lists every CBT API route the cbt.* operations in
// operations_cbt.go invoke — spec-style templates without the /api/v1
// prefix — with the query parameters panda forwards on each. The runtime
// path stays loosely typed on purpose (drift tolerance across mixed CBT
// versions); these tests catch panda drifting from the spec when the cbt
// dependency is bumped.
var cbtSpecRoutes = []struct {
	operation string
	path      string
	params    []string
}{
	{"cbt.list_models", "/models", []string{"type", "database", "search"}},
	{"cbt.list_external_models", "/models/external", []string{"database"}},
	{"cbt.get_external_model", "/models/external/{id}", nil},
	{"cbt.get_external_bounds", "/models/external/bounds", nil},
	{"cbt.get_external_bounds", "/models/external/{id}/bounds", nil},
	{"cbt.list_transformations", "/models/transformations", []string{"database", "type", "status"}},
	{"cbt.get_transformation", "/models/transformations/{id}", nil},
	{"cbt.get_transformation_coverage", "/models/transformations/coverage", []string{"database"}},
	{"cbt.get_transformation_coverage", "/models/transformations/{id}/coverage", nil},
	{"cbt.debug_coverage", "/models/transformations/{id}/coverage/{position}", nil},
	{"cbt.get_scheduled_runs", "/models/transformations/runs", []string{"database"}},
	{"cbt.get_scheduled_runs", "/models/transformations/{id}/runs", nil},
	{"cbt.get_interval_types", "/interval/types", nil},
}

func TestCBTRoutesMatchSpec(t *testing.T) {
	t.Parallel()

	spec, err := cbtapi.GetSwagger()
	require.NoError(t, err)

	for _, route := range cbtSpecRoutes {
		t.Run(route.operation+" "+route.path, func(t *testing.T) {
			t.Parallel()

			item := spec.Paths.Find(route.path)
			require.NotNil(t, item, "path %s missing from the CBT OpenAPI spec", route.path)

			operation := item.GetOperation(http.MethodGet)
			require.NotNil(t, operation, "path %s has no GET operation in the CBT OpenAPI spec", route.path)

			declared := make(map[string]bool, len(operation.Parameters))
			for _, ref := range operation.Parameters {
				if ref.Value != nil && ref.Value.In == "query" {
					declared[ref.Value.Name] = true
				}
			}

			for _, param := range route.params {
				assert.True(t, declared[param],
					"query param %q is not declared on GET %s in the CBT OpenAPI spec", param, route.path)
			}
		})
	}
}

// TestCBTSpecServesAPIV1 anchors the /api/v1 prefix panda hardcodes in front
// of every spec path.
func TestCBTSpecServesAPIV1(t *testing.T) {
	t.Parallel()

	spec, err := cbtapi.GetSwagger()
	require.NoError(t, err)

	require.NotEmpty(t, spec.Servers)

	for _, server := range spec.Servers {
		assert.True(t, strings.HasSuffix(server.URL, "/api/v1"),
			"CBT spec server %q is not rooted at /api/v1", server.URL)
	}
}
