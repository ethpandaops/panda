package ethnode

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/types"
)

func TestConfigIsEnabledDefaultsToTrue(t *testing.T) {
	t.Parallel()

	var cfg Config
	assert.True(t, cfg.IsEnabled())

	disabled := false
	cfg.Enabled = &disabled
	assert.False(t, cfg.IsEnabled())
}

func TestModuleInitFromDiscoveryRequiresEthnodeDatasource(t *testing.T) {
	t.Parallel()

	mod := New()

	require.NoError(t, mod.InitFromDiscovery([]types.DatasourceInfo{{Type: "ethnode", Name: "hoodi-node"}}))

	err := mod.InitFromDiscovery([]types.DatasourceInfo{{Type: "clickhouse", Name: "xatu"}})
	require.ErrorIs(t, err, module.ErrNoValidConfig)
}

func TestModuleValidateLoadsExamplesAndReturnsClone(t *testing.T) {
	t.Parallel()

	mod := New()
	require.NoError(t, mod.Validate())

	examples := mod.Examples()
	require.NotEmpty(t, examples)

	for key := range examples {
		delete(examples, key)
		break
	}

	assert.NotEmpty(t, mod.Examples())
}

func TestModuleDisabledSurfacesReturnNilOrEmpty(t *testing.T) {
	t.Parallel()

	mod := New()
	require.NoError(t, mod.Init([]byte("enabled: false")))

	env, err := mod.SandboxEnv()
	require.NoError(t, err)
	assert.Nil(t, env)
	assert.Nil(t, mod.Examples())
	assert.Nil(t, mod.PythonAPIDocs())
	assert.Empty(t, mod.GettingStartedSnippet())
}

func TestModuleEnabledSurfacesReturnDocsAndSandboxEnv(t *testing.T) {
	t.Parallel()

	mod := New()
	require.NoError(t, mod.Validate())

	env, err := mod.SandboxEnv()
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"ETHPANDAOPS_ETHNODE_AVAILABLE": "true"}, env)

	docs := mod.PythonAPIDocs()
	require.Contains(t, docs, "ethnode")
	assert.Contains(t, docs["ethnode"].Functions, "execution_rpc")
	assert.Contains(t, mod.GettingStartedSnippet(), "Ethereum Node API")
}
