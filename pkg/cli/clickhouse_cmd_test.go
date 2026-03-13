package cli

import (
	"net/http"
	"testing"

	"github.com/ethpandaops/panda/pkg/operations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClickHouseListDatasourcesUsesNameAsFallbackDescription(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/operations/clickhouse.list_datasources", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(t, w, http.StatusOK, operations.NewObjectResponse(operations.DatasourcesPayload{
			Datasources: []operations.Datasource{
				{Name: "xatu", Description: "", Database: ""},
			},
		}, nil))
	})

	newCLIHarness(t, mux)

	stdout, _ := captureOutput(t, func() {
		require.NoError(t, clickhouseListDatasourcesCmd.RunE(clickhouseListDatasourcesCmd, nil))
	})
	assert.Contains(t, stdout, "xatu")
}

func TestPrintClickHouseJSONFormatsObjectAndRawRows(t *testing.T) {
	tsv := []byte("name\tcount\nblocks\t5\n")

	stdout, _ := captureOutput(t, func() {
		require.NoError(t, printClickHouseJSON(tsv, false))
	})
	assert.Contains(t, stdout, `"columns": [`)
	assert.Contains(t, stdout, `"name": "blocks"`)

	stdout, _ = captureOutput(t, func() {
		require.NoError(t, printClickHouseJSON(tsv, true))
	})
	assert.Contains(t, stdout, `"rows": [`)
	assert.Contains(t, stdout, `"blocks"`)
}

func TestParseClickHouseTSVReturnsNilForWhitespaceInput(t *testing.T) {
	columns, rows, err := parseClickHouseTSV([]byte(" \n\t"))
	require.NoError(t, err)
	assert.Nil(t, columns)
	assert.Nil(t, rows)
}
