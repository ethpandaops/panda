package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/serverapi"
	"github.com/ethpandaops/panda/pkg/types"
)

func TestRunDatasourcesDefaultIsCompact(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/datasources" {
			http.NotFound(w, r)
			return
		}

		err := json.NewEncoder(w).Encode(serverapi.DatasourcesResponse{
			Datasources: []types.DatasourceInfo{{
				Type:        "clickhouse",
				Name:        "warehouse",
				Description: "long operational notes",
				Contents: []types.DatasetBinding{{
					Dataset: "metrics",
				}},
			}},
		})
		require.NoError(t, err)
	}))
	defer server.Close()

	setClientConfig(t, server.URL)
	setOutputFormat(t, "text")
	setDatasourcesFlags(t, "", false)

	output := captureStdout(t, func() {
		err := runDatasources(testCommand(), nil)
		require.NoError(t, err)
	})

	assert.Contains(t, output, "TYPE")
	assert.Contains(t, output, "DATASOURCE")
	assert.Contains(t, output, "DATASETS")
	assert.Contains(t, output, "clickhouse")
	assert.Contains(t, output, "warehouse")
	assert.Contains(t, output, "metrics")
	assert.NotContains(t, output, "DESCRIPTION")
	assert.NotContains(t, output, "long operational notes")
}

func TestRunDatasourcesDetailsIncludesDescription(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/datasources" {
			http.NotFound(w, r)
			return
		}

		err := json.NewEncoder(w).Encode(serverapi.DatasourcesResponse{
			Datasources: []types.DatasourceInfo{{
				Type:        "prometheus",
				Name:        "metrics",
				Description: "cluster notes",
			}},
		})
		require.NoError(t, err)
	}))
	defer server.Close()

	setClientConfig(t, server.URL)
	setOutputFormat(t, "text")
	setDatasourcesFlags(t, "", true)

	output := captureStdout(t, func() {
		err := runDatasources(testCommand(), nil)
		require.NoError(t, err)
	})

	assert.Contains(t, output, "DESCRIPTION")
	assert.Contains(t, output, "cluster notes")
}

func setDatasourcesFlags(t *testing.T, datasourceType string, details bool) {
	t.Helper()

	originalType := datasourcesType
	originalDetails := datasourcesDetails
	datasourcesType = datasourceType
	datasourcesDetails = details
	t.Cleanup(func() {
		datasourcesType = originalType
		datasourcesDetails = originalDetails
	})
}
