package cli

import (
	"net/http"
	"strings"
	"testing"

	clickhousemodule "github.com/ethpandaops/panda/modules/clickhouse"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListTablesSortsClustersAndMarksNetworkFilteredTables(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/resources/read", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "clickhouse://tables", r.URL.Query().Get("uri"))
		writeJSONResponse(t, w, http.StatusOK, clickhousemodule.TablesListResponse{
			Clusters: map[string]*clickhousemodule.ClusterTablesSummary{
				"zatu": {
					TableCount: 1,
					Tables: []*clickhousemodule.TableSummary{
						{Name: "tail", ColumnCount: 1},
					},
				},
				"xatu": {
					TableCount: 1,
					Tables: []*clickhousemodule.TableSummary{
						{Name: "blocks", ColumnCount: 2, HasNetworkCol: true},
					},
				},
			},
		})
	})

	newCLIHarness(t, mux)

	stdout, _ := captureOutput(t, func() {
		require.NoError(t, listTables(t.Context()))
	})
	assert.Less(t, strings.Index(stdout, "Cluster: xatu"), strings.Index(stdout, "Cluster: zatu"))
	assert.Contains(t, stdout, "blocks")
	assert.Contains(t, stdout, "network-filtered")
}

func TestShowTablePrintsCommentNetworksAndColumns(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/resources/read", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "clickhouse://tables/blocks", r.URL.Query().Get("uri"))
		writeJSONResponse(t, w, http.StatusOK, clickhousemodule.TableDetailResponse{
			Cluster: "xatu",
			Table: &clickhousemodule.TableSchema{
				Name:     "blocks",
				Comment:  "Beacon blocks",
				Networks: []string{"hoodi", "mainnet"},
				Columns: []clickhousemodule.TableColumn{
					{Name: "slot", Type: "UInt64"},
				},
			},
		})
	})

	newCLIHarness(t, mux)

	stdout, _ := captureOutput(t, func() {
		require.NoError(t, showTable(t.Context(), "blocks"))
	})
	assert.Contains(t, stdout, "Table: blocks  (cluster: xatu)")
	assert.Contains(t, stdout, "Comment: Beacon blocks")
	assert.Contains(t, stdout, "Networks: hoodi, mainnet")
	assert.Contains(t, stdout, `"slot"`)
}
