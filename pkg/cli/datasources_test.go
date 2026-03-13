package cli

import (
	"net/http"
	"testing"

	"github.com/ethpandaops/panda/pkg/serverapi"
	"github.com/ethpandaops/panda/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunDatasourcesPrintsFriendlyAndJSONOutput(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/datasources", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(t, w, http.StatusOK, serverapi.DatasourcesResponse{
			Datasources: []types.DatasourceInfo{
				{Type: "clickhouse", Name: "xatu", Description: ""},
			},
		})
	})

	newCLIHarness(t, mux)

	stdout, _ := captureOutput(t, func() {
		require.NoError(t, runDatasources(datasourcesCmd, nil))
	})
	assert.Contains(t, stdout, "clickhouse")
	assert.Contains(t, stdout, "xatu")

	datasourcesJSON = true
	stdout, _ = captureOutput(t, func() {
		require.NoError(t, runDatasources(datasourcesCmd, nil))
	})
	assert.Contains(t, stdout, `"datasources"`)
	assert.Contains(t, stdout, `"xatu"`)
}

func TestPrintJSONReturnsMarshalError(t *testing.T) {
	err := printJSON(map[string]any{"bad": func() {}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "marshaling JSON")
}
