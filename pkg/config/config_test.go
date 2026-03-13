package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ethpandaops/panda/pkg/configutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadAppliesDefaultsAndValidates(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
sandbox:
  image: sandbox:test
`), 0o600))

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, path, cfg.Path())
	assert.Equal(t, "stdio", cfg.Server.Transport)
	assert.Equal(t, "docker", cfg.Sandbox.Backend)
	assert.Equal(t, 60, cfg.Sandbox.Timeout)
	assert.Equal(t, "2g", cfg.Sandbox.MemoryLimit)
	assert.Equal(t, 1.0, cfg.Sandbox.CPULimit)
	assert.Equal(t, 30*time.Minute, cfg.Sandbox.Sessions.TTL)
	assert.Equal(t, 4*time.Hour, cfg.Sandbox.Sessions.MaxDuration)
	assert.Equal(t, 10, cfg.Sandbox.Sessions.MaxSessions)
	assert.Equal(t, 2490, cfg.Observability.MetricsPort)
	assert.Equal(t, "http://localhost:18081", cfg.Proxy.URL)
	assert.NotEmpty(t, cfg.Storage.BaseDir)
}

func TestLoadRejectsUnknownFieldsAndInvalidConfig(t *testing.T) {
	t.Parallel()

	t.Run("unknown field", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.yaml")
		require.NoError(t, os.WriteFile(path, []byte(`
sandbox:
  image: sandbox:test
unknown_field: true
`), 0o600))

		_, err := Load(path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parsing config")
	})

	t.Run("invalid sandbox timeout", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.yaml")
		require.NoError(t, os.WriteFile(path, []byte(`
sandbox:
  image: sandbox:test
  timeout: 601
`), 0o600))

		_, err := Load(path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "sandbox.timeout cannot exceed 600 seconds")
	})
}

func TestSessionConfigServerURLValidateAndSubstituteEnvVars(t *testing.T) {
	var sessionCfg SessionConfig
	assert.True(t, sessionCfg.IsEnabled())

	disabled := false
	sessionCfg.Enabled = &disabled
	assert.False(t, sessionCfg.IsEnabled())

	assert.Equal(t, "", (*Config)(nil).ServerURL())
	assert.Equal(t, "https://server.example", (&Config{
		Server: ServerConfig{URL: "https://server.example/"},
	}).ServerURL())
	assert.Equal(t, "https://base.example", (&Config{
		Server: ServerConfig{BaseURL: "https://base.example/"},
	}).ServerURL())
	assert.Equal(t, "http://localhost:2480", (&Config{
		Server: ServerConfig{Host: "0.0.0.0"},
	}).ServerURL())
	assert.Equal(t, "http://[2001:db8::1]:3030", (&Config{
		Server: ServerConfig{Host: "2001:db8::1", Port: 3030},
	}).ServerURL())

	cfg := &Config{}
	assert.EqualError(t, cfg.Validate(), "sandbox.image is required")

	cfg.Sandbox.Image = "sandbox:test"
	cfg.Sandbox.Timeout = MaxSandboxTimeout + 1
	assert.EqualError(t, cfg.Validate(), "sandbox.timeout cannot exceed 600 seconds")

	cfg.Sandbox.Timeout = 60
	cfg.Proxy.URL = ""
	assert.EqualError(t, cfg.Validate(), "proxy.url is required")

	t.Setenv("PANDA_PROXY_URL", "http://proxy.example")
	substituted, err := configutil.SubstituteEnvVars(stringsJoin(
		"# ${IGNORED}",
		"proxy_url: ${PANDA_PROXY_URL}",
		"missing: ${MISSING:-fallback}",
		"empty: ${EMPTY_VALUE}",
	))
	require.NoError(t, err)
	assert.Contains(t, substituted, "proxy_url: http://proxy.example")
	assert.Contains(t, substituted, "missing: fallback")
	assert.Contains(t, substituted, "# ${IGNORED}")
}

func TestApplyDefaultsDirectly(t *testing.T) {
	t.Parallel()

	cfg := &Config{}
	applyDefaults(cfg)

	assert.Equal(t, "0.0.0.0", cfg.Server.Host)
	assert.Equal(t, 2480, cfg.Server.Port)
	assert.Equal(t, "stdio", cfg.Server.Transport)
	assert.Equal(t, "docker", cfg.Sandbox.Backend)
	assert.Equal(t, "2g", cfg.Sandbox.MemoryLimit)
	assert.Equal(t, 1.0, cfg.Sandbox.CPULimit)
}

func stringsJoin(lines ...string) string {
	result := ""
	for i, line := range lines {
		if i > 0 {
			result += "\n"
		}
		result += line
	}

	return result
}
