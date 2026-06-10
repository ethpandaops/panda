package configpath

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveAppConfigPath(t *testing.T) {
	t.Run("explicit path wins and is cleaned", func(t *testing.T) {
		got, err := ResolveAppConfigPath("./some/../config.yaml")
		require.NoError(t, err)
		assert.Equal(t, "config.yaml", got)
	})

	t.Run("env var precedence: PANDA_CONFIG over CONFIG_PATH", func(t *testing.T) {
		t.Setenv("PANDA_CONFIG", "/from/panda.yaml")
		t.Setenv("ETHPANDAOPS_CONFIG", "/from/ethpandaops.yaml")
		t.Setenv("CONFIG_PATH", "/from/generic.yaml")

		got, err := ResolveAppConfigPath("")
		require.NoError(t, err)
		assert.Equal(t, "/from/panda.yaml", got)
	})

	t.Run("generic CONFIG_PATH honored for app config", func(t *testing.T) {
		t.Setenv("PANDA_CONFIG", "")
		t.Setenv("ETHPANDAOPS_CONFIG", "")
		t.Setenv("CONFIG_PATH", "/from/generic.yaml")

		got, err := ResolveAppConfigPath("")
		require.NoError(t, err)
		assert.Equal(t, "/from/generic.yaml", got)
	})

	t.Run("not found returns NotFoundError", func(t *testing.T) {
		t.Setenv("PANDA_CONFIG", "")
		t.Setenv("ETHPANDAOPS_CONFIG", "")
		t.Setenv("CONFIG_PATH", "")
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		t.Setenv("HOME", t.TempDir())

		_, err := ResolveAppConfigPath("")
		var notFound *NotFoundError
		assert.ErrorAs(t, err, &notFound)
	})
}

func TestResolveProxyConfigPath(t *testing.T) {
	t.Run("explicit relative path joined to baseDir", func(t *testing.T) {
		got, err := ResolveProxyConfigPath("proxy-config.yaml", "/base")
		require.NoError(t, err)
		assert.Equal(t, filepath.Join("/base", "proxy-config.yaml"), got)
	})

	t.Run("dedicated env var honored", func(t *testing.T) {
		t.Setenv("PANDA_PROXY_CONFIG", "/from/proxy.yaml")

		got, err := ResolveProxyConfigPath("", "")
		require.NoError(t, err)
		assert.Equal(t, "/from/proxy.yaml", got)
	})

	t.Run("generic CONFIG_PATH does not leak into proxy resolution", func(t *testing.T) {
		t.Setenv("PANDA_PROXY_CONFIG", "")
		t.Setenv("ETHPANDAOPS_PROXY_CONFIG", "")
		t.Setenv("CONFIG_PATH", "/from/app.yaml")
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		t.Setenv("HOME", t.TempDir())

		_, err := ResolveProxyConfigPath("", "")
		var notFound *NotFoundError
		assert.ErrorAs(t, err, &notFound)
	})
}

func TestDedupe(t *testing.T) {
	got := dedupe([]string{"a.yaml", "", "./a.yaml", "b.yaml"})
	assert.Equal(t, []string{"a.yaml", "b.yaml"}, got)
}
