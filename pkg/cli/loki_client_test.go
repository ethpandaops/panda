package cli

import (
	"net/http"
	"testing"

	"github.com/ethpandaops/panda/pkg/operations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLokiClientHelpers(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/operations/loki.list_datasources", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(t, w, http.StatusOK, operations.NewObjectResponse(operations.DatasourcesPayload{
			Datasources: []operations.Datasource{{Name: "logs", URL: "https://logs.example"}},
		}, nil))
	})
	for _, path := range []string{
		"/api/v1/operations/loki.query",
		"/api/v1/operations/loki.query_instant",
		"/api/v1/operations/loki.get_labels",
		"/api/v1/operations/loki.get_label_values",
	} {
		mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`{"status":"success"}`))
			require.NoError(t, err)
		})
	}

	newCLIHarness(t, mux)

	datasources, err := listLokiDatasources()
	require.NoError(t, err)
	require.Len(t, datasources, 1)
	assert.Equal(t, "logs", datasources[0].Name)

	query, err := lokiQuery(operations.LokiQueryArgs{Datasource: "logs", Query: "{job=\"api\"}"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"status":"success"}`, string(query.Body))

	instant, err := lokiInstantQuery(operations.LokiInstantQueryArgs{Datasource: "logs", Query: "{job=\"api\"}"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"status":"success"}`, string(instant.Body))

	labels, err := lokiLabels(operations.LokiLabelsArgs{Datasource: "logs"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"status":"success"}`, string(labels.Body))

	values, err := lokiLabelValues(operations.LokiLabelValuesArgs{Datasource: "logs", Label: "job"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"status":"success"}`, string(values.Body))
}
