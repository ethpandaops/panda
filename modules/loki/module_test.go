package loki

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/types"
)

func TestModuleInitFromDiscoveryFiltersLokiDatasources(t *testing.T) {
	t.Parallel()

	mod := New()
	require.NoError(t, mod.InitFromDiscovery([]types.DatasourceInfo{
		{Type: "clickhouse", Name: "xatu"},
		{Type: "loki", Name: "logs"},
	}))
	require.Len(t, mod.datasources, 1)
	assert.Equal(t, "logs", mod.datasources[0].Name)

	err := mod.InitFromDiscovery([]types.DatasourceInfo{{Type: "prometheus", Name: "metrics"}})
	require.ErrorIs(t, err, module.ErrNoValidConfig)
}

func TestModuleInitValidateAndExamples(t *testing.T) {
	t.Parallel()

	mod := New()
	err := mod.Init([]byte(`
instances:
  - name: ""
    url: https://ignored.example
  - name: logs
    description: Main logs
    url: https://logs.example
`))
	require.NoError(t, err)
	require.Len(t, mod.datasources, 1)
	assert.Equal(t, "loki", mod.datasources[0].Type)
	assert.Equal(t, "https://logs.example", mod.datasources[0].Metadata["url"])

	require.NoError(t, mod.Validate())
	examples := mod.Examples()
	require.NotEmpty(t, examples)
	assert.Contains(t, mod.PythonAPIDocs(), "loki")

	for key := range examples {
		delete(examples, key)
		break
	}
	assert.NotEmpty(t, mod.Examples())
}

func TestModuleInitRejectsNamelessConfigAndValidateRejectsDuplicates(t *testing.T) {
	t.Parallel()

	mod := New()
	err := mod.Init([]byte(`
instances:
  - name: ""
    url: https://ignored.example
`))
	require.ErrorIs(t, err, module.ErrNoValidConfig)

	mod.datasources = []types.DatasourceInfo{
		{Type: "loki", Name: "logs"},
		{Type: "loki", Name: "logs"},
	}
	err = mod.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicated")
}
