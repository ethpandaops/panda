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
