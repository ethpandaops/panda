package prometheus

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/types"
)

func TestModuleInitFromDiscoveryFiltersPrometheusDatasources(t *testing.T) {
	t.Parallel()

	mod := New()
	require.NoError(t, mod.InitFromDiscovery([]types.DatasourceInfo{
		{Type: "loki", Name: "logs"},
		{Type: "prometheus", Name: "metrics"},
	}))
	require.Len(t, mod.datasources, 1)
	assert.Equal(t, "metrics", mod.datasources[0].Name)

	err := mod.InitFromDiscovery([]types.DatasourceInfo{{Type: "clickhouse", Name: "xatu"}})
	require.ErrorIs(t, err, module.ErrNoValidConfig)
}

func TestModuleInitValidateAndExamples(t *testing.T) {
	t.Parallel()

	mod := New()
	err := mod.Init([]byte(`
instances:
  - name: ""
    url: https://ignored.example
  - name: metrics
    description: Main metrics
    url: https://metrics.example
`))
	require.NoError(t, err)
	require.Len(t, mod.datasources, 1)
	assert.Equal(t, "prometheus", mod.datasources[0].Type)
	assert.Equal(t, "https://metrics.example", mod.datasources[0].Metadata["url"])

	require.NoError(t, mod.Validate())
	examples := mod.Examples()
	require.NotEmpty(t, examples)
	assert.Contains(t, mod.PythonAPIDocs(), "prometheus")

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
		{Type: "prometheus", Name: "metrics"},
		{Type: "prometheus", Name: "metrics"},
	}
	err = mod.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicated")
}
