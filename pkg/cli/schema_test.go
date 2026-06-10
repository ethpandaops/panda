package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	clickhousemodule "github.com/ethpandaops/panda/modules/clickhouse"
)

func TestSplitQualifiedTable(t *testing.T) {
	database, table, ok := splitQualifiedTable("mainnet.blocks")
	require.True(t, ok)
	assert.Equal(t, "mainnet", database)
	assert.Equal(t, "blocks", table)

	_, _, ok = splitQualifiedTable("mainnet")
	assert.False(t, ok)

	_, _, ok = splitQualifiedTable("mainnet.blocks.extra")
	assert.False(t, ok)
}

func TestFilterTablesByNameMatchesQualifiedName(t *testing.T) {
	tables := []*clickhousemodule.TableSummary{
		{Database: "db", Name: "blocks", ColumnCount: 2},
		{Database: "metrics", Name: "validators", ColumnCount: 3},
		nil,
	}

	filtered := filterTablesByName(tables, "METRICS.")

	require.Len(t, filtered, 1)
	assert.Equal(t, "validators", filtered[0].Name)
}

func TestRenderTablesListSummarizesClusterDatabases(t *testing.T) {
	setOutputFormat(t, "text")

	output := captureStdout(t, func() {
		err := renderTablesList(&clickhousemodule.TablesListResponse{
			Clusters: map[string]*clickhousemodule.ClusterTablesSummary{
				"warehouse": {
					TableCount:  3,
					LastUpdated: "now",
					Tables: []*clickhousemodule.TableSummary{
						{Database: "db_b", Name: "blocks"},
						{Database: "db_a", Name: "blocks"},
						{Database: "db_a", Name: "validators"},
					},
				},
			},
		}, true)
		require.NoError(t, err)
	})

	assert.Contains(t, output, "3 tables across 2 databases")
	assert.Contains(t, output, "DATABASE")
	assert.Contains(t, output, "db_a")
	assert.Contains(t, output, "Use: panda schema warehouse <database>")
	assert.NotContains(t, output, "db_a.blocks")
}

func TestRunSchemaAcceptsQualifiedTableAndPrintsKeys(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/resources/read" {
			http.NotFound(w, r)
			return
		}

		assert.Equal(t, "clickhouse://tables/clickhouse-refined/mainnet/blocks", r.URL.Query().Get("uri"))

		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(clickhousemodule.TableDetailResponse{
			Cluster: "clickhouse-refined",
			Table: &clickhousemodule.TableSchema{
				Database:    "mainnet",
				Name:        "blocks",
				Engine:      "MergeTree",
				PartitionBy: "toYYYYMM(slot_time)",
				OrderBy:     "(slot_time, root)",
				PrimaryKey:  "slot_time",
				Columns: []clickhousemodule.TableColumn{{
					Name: "slot_time",
					Type: "DateTime",
				}},
			},
		})
		require.NoError(t, err)
	}))
	defer server.Close()

	setClientConfig(t, server.URL)
	setOutputFormat(t, "text")

	output := captureStdout(t, func() {
		err := runSchema(testCommand(), []string{"clickhouse-refined", "mainnet.blocks"})
		require.NoError(t, err)
	})

	assert.Contains(t, output, "Table: mainnet.blocks")
	assert.Contains(t, output, "Engine: MergeTree")
	assert.Contains(t, output, "Keys:")
	assert.Contains(t, output, "Partition by: toYYYYMM(slot_time)")
	assert.Contains(t, output, "Order by: (slot_time, root)")
	assert.Contains(t, output, "Primary key: slot_time")
}
