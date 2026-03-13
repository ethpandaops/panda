package cli

import (
	"net/http"
	"testing"

	"github.com/ethpandaops/panda/pkg/operations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLokiListDatasourcesShowsEmptyMessage(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/operations/loki.list_datasources", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(t, w, http.StatusOK, operations.NewObjectResponse(operations.DatasourcesPayload{}, nil))
	})

	newCLIHarness(t, mux)

	stdout, _ := captureOutput(t, func() {
		require.NoError(t, lokiListDatasourcesCmd.RunE(lokiListDatasourcesCmd, nil))
	})
	assert.Contains(t, stdout, "No Loki datasources found.")
}

func TestPrintLokiResultSkipsShortEntriesAndFallsBackToRawPayload(t *testing.T) {
	stdout, _ := captureOutput(t, func() {
		require.NoError(t, printLokiResult([]byte(`{"data":{"result":[{"values":[["1","line one"],["2"]]}]}}`)))
	})
	assert.Contains(t, stdout, "line one")
	assert.NotContains(t, stdout, "\n2\n")

	stdout, _ = captureOutput(t, func() {
		require.NoError(t, printLokiResult([]byte("not-json")))
	})
	assert.Equal(t, "not-json\n", stdout)
}
