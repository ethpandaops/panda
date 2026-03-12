package cli

import (
	"net/http"
	"testing"

	"github.com/ethpandaops/panda/pkg/serverapi"
	"github.com/ethpandaops/panda/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunDocsPrintsModuleListAndSelectedModule(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/resources/read", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(t, w, http.StatusOK, serverapi.APIDocResponse{
			Library: "ethpandaops",
			Modules: map[string]types.ModuleDoc{
				"clickhouse": {
					Description: "Query ClickHouse",
					Functions: map[string]types.FunctionDoc{
						"query": {
							Signature:   "query(sql: str) -> DataFrame",
							Description: "Execute SQL",
							Parameters:  map[string]string{"sql": "SQL query"},
						},
					},
				},
				"dora": {Description: "Query Dora"},
			},
		})
	})

	newCLIHarness(t, mux)

	stdout, _ := captureOutput(t, func() {
		require.NoError(t, runDocs(docsCmd, nil))
	})
	assert.Contains(t, stdout, "Available modules:")
	assert.Contains(t, stdout, "clickhouse")
	assert.Contains(t, stdout, "dora")

	stdout, _ = captureOutput(t, func() {
		require.NoError(t, runDocs(docsCmd, []string{"clickhouse"}))
	})
	assert.Contains(t, stdout, "Module: clickhouse")
	assert.Contains(t, stdout, "query(sql: str) -> DataFrame")
	assert.Contains(t, stdout, "Parameters:")
}

func TestRunDocsJSONAndMissingModuleErrors(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/resources/read", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(t, w, http.StatusOK, serverapi.APIDocResponse{
			Library: "ethpandaops",
			Modules: map[string]types.ModuleDoc{
				"clickhouse": {Description: "Query ClickHouse"},
			},
		})
	})

	newCLIHarness(t, mux)

	docsJSON = true
	stdout, _ := captureOutput(t, func() {
		require.NoError(t, runDocs(docsCmd, nil))
	})
	assert.Contains(t, stdout, `"clickhouse"`)

	err := runDocs(docsCmd, []string{"missing"})
	require.Error(t, err)
	assert.EqualError(t, err, `module "missing" not found`)
}
