package cli

import (
	"net/http"
	"testing"

	"github.com/ethpandaops/panda/pkg/operations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrometheusClientHelpers(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/operations/prometheus.list_datasources", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(t, w, http.StatusOK, operations.NewObjectResponse(operations.DatasourcesPayload{
			Datasources: []operations.Datasource{{Name: "metrics", URL: "https://prom.example"}},
		}, nil))
	})
	for _, path := range []string{
		"/api/v1/operations/prometheus.query",
		"/api/v1/operations/prometheus.query_range",
		"/api/v1/operations/prometheus.get_labels",
		"/api/v1/operations/prometheus.get_label_values",
	} {
		mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`{"status":"success"}`))
			require.NoError(t, err)
		})
	}

	newCLIHarness(t, mux)

	datasources, err := listPrometheusDatasources()
	require.NoError(t, err)
	require.Len(t, datasources, 1)
	assert.Equal(t, "metrics", datasources[0].Name)

	query, err := prometheusQuery(operations.PrometheusQueryArgs{Datasource: "metrics", Query: "up"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"status":"success"}`, string(query.Body))

	queryRange, err := prometheusQueryRange(operations.PrometheusRangeQueryArgs{Datasource: "metrics", Query: "up", Start: "0", End: "1", Step: "1m"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"status":"success"}`, string(queryRange.Body))

	labels, err := prometheusLabels(operations.DatasourceArgs{Datasource: "metrics"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"status":"success"}`, string(labels.Body))

	values, err := prometheusLabelValues(operations.DatasourceLabelArgs{Datasource: "metrics", Label: "job"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"status":"success"}`, string(values.Body))
}
