package cli

import (
	"net/http"
	"testing"

	"github.com/ethpandaops/panda/pkg/operations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrometheusListDatasourcesShowsEmptyMessage(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/operations/prometheus.list_datasources", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(t, w, http.StatusOK, operations.NewObjectResponse(operations.DatasourcesPayload{}, nil))
	})

	newCLIHarness(t, mux)

	stdout, _ := captureOutput(t, func() {
		require.NoError(t, promListDatasourcesCmd.RunE(promListDatasourcesCmd, nil))
	})
	assert.Contains(t, stdout, "No Prometheus datasources found.")
}

func TestPrintPromResultHandlesNonSuccessAndMatrixValues(t *testing.T) {
	stdout, _ := captureOutput(t, func() {
		require.NoError(t, printPromResult([]byte(`{"status":"error","error":"broken"}`)))
	})
	assert.Contains(t, stdout, `"status": "error"`)

	stdout, _ = captureOutput(t, func() {
		require.NoError(t, printPromResult([]byte(`{"status":"success","data":{"result":[{"metric":{"job":"panda"},"values":[[1,"1"],[2,"2"]]}]}}`)))
	})
	assert.Contains(t, stdout, `{job="panda"}:`)
	assert.Contains(t, stdout, `1970-01-01T00:00:01Z => 1`)
	assert.Contains(t, stdout, `1970-01-01T00:00:02Z => 2`)
}
