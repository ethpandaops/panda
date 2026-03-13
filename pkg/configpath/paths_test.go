package configpath

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfigPathsAndResolution(t *testing.T) {
	t.Run("default config dir uses XDG config home", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
		assert.Equal(t, filepath.Join("/tmp/xdg", "panda"), DefaultConfigDir())
		assert.Equal(t, filepath.Join("/tmp/xdg", "panda", "config.yaml"), DefaultAppConfigPath())
		assert.Equal(t, filepath.Join("/tmp/xdg", "panda", "proxy-config.yaml"), DefaultProxyConfigPath())
	})

	t.Run("app config resolution uses explicit path env and file discovery", func(t *testing.T) {
		dir := t.TempDir()
		explicit := filepath.Join(dir, "..", "config.yaml")
		resolved, err := ResolveAppConfigPath(explicit)
		require.NoError(t, err)
		assert.Equal(t, filepath.Clean(explicit), resolved)

		t.Setenv("PANDA_CONFIG", filepath.Join(dir, "env-config.yaml"))
		resolved, err = ResolveAppConfigPath("")
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(dir, "env-config.yaml"), resolved)

		t.Setenv("PANDA_CONFIG", "")
		t.Setenv("ETHPANDAOPS_CONFIG", "")
		t.Setenv("CONFIG_PATH", "")
		t.Setenv("XDG_CONFIG_HOME", dir)
		configPath := filepath.Join(dir, "panda", "config.yaml")
		require.NoError(t, os.MkdirAll(filepath.Dir(configPath), 0o755))
		require.NoError(t, os.WriteFile(configPath, []byte("server: {}\n"), 0o600))

		resolved, err = ResolveAppConfigPath("")
		require.NoError(t, err)
		assert.Equal(t, configPath, resolved)
	})

	t.Run("proxy config resolution handles relative explicit env and defaults", func(t *testing.T) {
		dir := t.TempDir()

		resolved, err := ResolveProxyConfigPath("configs/proxy.yaml", dir)
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(dir, "configs", "proxy.yaml"), resolved)

		t.Setenv("PANDA_PROXY_CONFIG", filepath.Join(dir, "env-proxy.yaml"))
		resolved, err = ResolveProxyConfigPath("", dir)
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(dir, "env-proxy.yaml"), resolved)

		t.Setenv("PANDA_PROXY_CONFIG", "")
		t.Setenv("ETHPANDAOPS_PROXY_CONFIG", "")
		t.Setenv("CONFIG_PATH", "")
		require.NoError(t, os.WriteFile(filepath.Join(dir, "proxy-config.yaml"), []byte("proxy: {}\n"), 0o600))

		resolved, err = ResolveProxyConfigPath("", dir)
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(dir, "proxy-config.yaml"), resolved)
	})
}

func TestNotFoundErrorAndHelpers(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(existing, []byte("ok"), 0o600))

	err := (&NotFoundError{
		Kind:       "panda config",
		Searched:   []string{"/tmp/a", "/tmp/b"},
		Suggestion: "Run panda init.",
	}).Error()
	assert.Contains(t, err, "looked in: /tmp/a, /tmp/b")

	err = (&NotFoundError{
		Kind:       "panda proxy config",
		Suggestion: "Create one.",
	}).Error()
	assert.Equal(t, "no panda proxy config found. Create one.", err)

	assert.Equal(t, filepath.Join(dir, "relative.yaml"), cleanRelative(dir, "relative.yaml"))
	assert.Equal(t, "/tmp/absolute.yaml", cleanRelative(dir, "/tmp/absolute.yaml"))

	first, ok := firstExisting([]string{filepath.Join(dir, "missing.yaml"), existing, existing})
	assert.True(t, ok)
	assert.Equal(t, existing, first)

	assert.Equal(t, []string{existing}, dedupe([]string{"", existing, existing}))
}
