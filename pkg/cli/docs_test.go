package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/types"
)

func TestSplitDocFunction(t *testing.T) {
	moduleName, functionName, ok := splitDocFunction("clickhouse.query")
	require.True(t, ok)
	assert.Equal(t, "clickhouse", moduleName)
	assert.Equal(t, "query", functionName)

	_, _, ok = splitDocFunction("clickhouse")
	assert.False(t, ok)

	_, _, ok = splitDocFunction("clickhouse.query.extra")
	assert.False(t, ok)
}

func TestShowFunction(t *testing.T) {
	docs := map[string]types.ModuleDoc{
		"clickhouse": {
			Functions: map[string]types.FunctionDoc{
				"query": {
					Signature:   "query(datasource, sql)",
					Description: "Execute SQL",
				},
			},
		},
	}

	output := captureStdout(t, func() {
		err := showFunction(docs, "clickhouse", "query")
		require.NoError(t, err)
	})

	assert.Contains(t, output, "Function: clickhouse.query")
	assert.Contains(t, output, "query(datasource, sql)")
}

func TestShowModuleExplainsCLICommandName(t *testing.T) {
	err := showModule(map[string]types.ModuleDoc{
		"clickhouse": {Description: "SQL"},
	}, "execute")

	require.Error(t, err)
	assert.Contains(t, err.Error(), `module "execute" not found in Python API docs`)
	assert.Contains(t, err.Error(), `panda execute --help`)
	assert.Contains(t, err.Error(), "Available Python modules: clickhouse")
}

func TestShowModuleListsPythonModulesForUnknownName(t *testing.T) {
	err := showModule(map[string]types.ModuleDoc{
		"clickhouse": {Description: "SQL"},
	}, "missing")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "Available Python modules: clickhouse")
	assert.NotContains(t, err.Error(), "--help")
}
