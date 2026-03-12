package cli

import (
	"context"
	"net/http"
	"testing"

	clickhousemodule "github.com/ethpandaops/panda/modules/clickhouse"
	"github.com/ethpandaops/panda/pkg/operations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClickHouseClientHelpers(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/operations/clickhouse.list_datasources", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(t, w, http.StatusOK, operations.NewObjectResponse(operations.DatasourcesPayload{
			Datasources: []operations.Datasource{{Name: "xatu", Database: "default"}},
		}, nil))
	})
	mux.HandleFunc("/api/v1/operations/clickhouse.query", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, err := w.Write([]byte("rows"))
		require.NoError(t, err)
	})
	mux.HandleFunc("/api/v1/operations/clickhouse.query_raw", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		_, err := w.Write([]byte("slot\n1\n"))
		require.NoError(t, err)
	})
	mux.HandleFunc("/api/v1/resources/read", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("uri") {
		case "clickhouse://tables":
			writeJSONResponse(t, w, http.StatusOK, clickhousemodule.TablesListResponse{
				Clusters: map[string]*clickhousemodule.ClusterTablesSummary{
					"xatu": {TableCount: 1},
				},
			})
		case "clickhouse://tables/blocks":
			writeJSONResponse(t, w, http.StatusOK, clickhousemodule.TableDetailResponse{
				Cluster: "xatu",
				Table:   &clickhousemodule.TableSchema{Name: "blocks"},
			})
		default:
			http.NotFound(w, r)
		}
	})

	newCLIHarness(t, mux)

	datasources, err := listClickHouseDatasources()
	require.NoError(t, err)
	require.Len(t, datasources, 1)
	assert.Equal(t, "xatu", datasources[0].Name)

	queryResponse, err := clickHouseQuery(context.Background(), operations.ClickHouseQueryArgs{Datasource: "xatu", SQL: "SELECT 1"})
	require.NoError(t, err)
	assert.Equal(t, "text/plain", queryResponse.ContentType)
	assert.Equal(t, "rows", string(queryResponse.Body))

	rawResponse, err := clickHouseQueryRaw(context.Background(), operations.ClickHouseQueryArgs{Datasource: "xatu", SQL: "SELECT 1"})
	require.NoError(t, err)
	assert.Equal(t, "text/csv", rawResponse.ContentType)
	assert.Equal(t, "slot\n1\n", string(rawResponse.Body))

	tables, err := readClickHouseTables(context.Background())
	require.NoError(t, err)
	assert.Contains(t, tables.Clusters, "xatu")

	table, err := readClickHouseTable(context.Background(), "blocks")
	require.NoError(t, err)
	require.NotNil(t, table.Table)
	assert.Equal(t, "blocks", table.Table.Name)
}
