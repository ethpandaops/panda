package app

import (
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/config"
)

func TestBuildSandboxEnvInitializesEmptyModuleEnv(t *testing.T) {
	t.Parallel()

	logger := logrus.New()
	moduleStub := &fakeModule{name: "fake", calls: &[]string{}}
	app := New(logger, &config.Config{
		Server: config.ServerConfig{SandboxURL: " https://sandbox.example/ "},
	})
	app.ModuleRegistry = newInitializedRegistry(t, logger, moduleStub)

	env, err := app.BuildSandboxEnv()
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"ETHPANDAOPS_API_URL": "https://sandbox.example",
	}, env)
}

func TestBuildSandboxEnvUsesServerURLFallback(t *testing.T) {
	t.Parallel()

	logger := logrus.New()
	moduleStub := &fakeModule{
		name:  "fake",
		env:   map[string]string{"MODULE_ENV": "value"},
		calls: &[]string{},
	}
	app := New(logger, &config.Config{
		Server: config.ServerConfig{URL: " https://server.example/ "},
	})
	app.ModuleRegistry = newInitializedRegistry(t, logger, moduleStub)

	env, err := app.BuildSandboxEnv()
	require.NoError(t, err)
	assert.Equal(t, "value", env["MODULE_ENV"])
	assert.Equal(t, "https://server.example", env["ETHPANDAOPS_API_URL"])
}
