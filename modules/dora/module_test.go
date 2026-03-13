package dora

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDoraConfigExamplesAndModuleBehavior(t *testing.T) {
	var cfg Config
	assert.True(t, cfg.IsEnabled())
	disabled := false
	cfg.Enabled = &disabled
	assert.False(t, cfg.IsEnabled())

	firstExamples, err := loadExamples()
	require.NoError(t, err)
	secondExamples, err := loadExamples()
	require.NoError(t, err)
	require.NotEmpty(t, firstExamples)
	require.NotEmpty(t, secondExamples)

	module := New()
	assert.Equal(t, "dora", module.Name())
	assert.True(t, module.DefaultEnabled())
	assert.True(t, module.Enabled())

	require.NoError(t, module.Init(nil))
	require.NoError(t, module.Validate())
	assert.NotEmpty(t, module.Examples())
	assert.Contains(t, module.PythonAPIDocs(), "dora")
	assert.Contains(t, module.GettingStartedSnippet(), "Dora Beacon Chain Explorer")

	env, err := module.SandboxEnv()
	require.NoError(t, err)
	assert.Nil(t, env)

	require.NoError(t, module.Init([]byte("enabled: false\n")))
	require.NoError(t, module.Validate())
	assert.False(t, module.Enabled())
	assert.Nil(t, module.Examples())
	assert.Nil(t, module.PythonAPIDocs())
	assert.Empty(t, module.GettingStartedSnippet())
	env, err = module.SandboxEnv()
	require.NoError(t, err)
	assert.Nil(t, env)
}
