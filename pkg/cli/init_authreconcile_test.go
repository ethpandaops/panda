package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const staleDexConfig = `# panda configuration
server:
  base_url: "http://localhost:2480"

storage:
  base_dir: "/data/storage"

proxy:
  url: "https://panda-proxy.ethpandaops.io"
  auth:
    issuer_url: "https://dex.example.com"
    client_id: "panda"
`

func TestReconcileProxyAuth(t *testing.T) {
	t.Parallel()

	authentik := initAuthConfig{
		Mode:      "oidc",
		IssuerURL: "https://authentik.example.com/application/o/panda-proxy/",
		ClientID:  "panda-proxy",
	}

	t.Run("rewrites a stale issuer and preserves the rest", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "config.yaml")
		require.NoError(t, os.WriteFile(path, []byte(staleDexConfig), 0o644))

		changed, err := reconcileProxyAuth(path, authentik)
		require.NoError(t, err)
		assert.True(t, changed, "stale config should be reconciled")

		data, err := os.ReadFile(path)
		require.NoError(t, err)

		var parsed map[string]any
		require.NoError(t, yaml.Unmarshal(data, &parsed))

		proxy, ok := parsed["proxy"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "https://panda-proxy.ethpandaops.io", proxy["url"],
			"unrelated proxy fields must be preserved")

		auth, ok := proxy["auth"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, authentik.IssuerURL, auth["issuer_url"])
		assert.Equal(t, authentik.ClientID, auth["client_id"])
		assert.Equal(t, "oidc", auth["mode"])

		// Unrelated sections are untouched.
		server, ok := parsed["server"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "http://localhost:2480", server["base_url"])
	})

	t.Run("is a no-op when already current", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "config.yaml")
		require.NoError(t, os.WriteFile(path, []byte(staleDexConfig), 0o644))

		// First reconcile brings it current; second must report no change.
		_, err := reconcileProxyAuth(path, authentik)
		require.NoError(t, err)

		before, err := os.ReadFile(path)
		require.NoError(t, err)

		changed, err := reconcileProxyAuth(path, authentik)
		require.NoError(t, err)
		assert.False(t, changed, "matching config should not be rewritten")

		after, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, string(before), string(after), "file must be byte-identical when unchanged")
	})

	t.Run("drops mode when reverting to the oauth default", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "config.yaml")
		require.NoError(t, os.WriteFile(path, []byte(staleDexConfig), 0o644))

		// Bring it to oidc first.
		_, err := reconcileProxyAuth(path, authentik)
		require.NoError(t, err)

		legacy := initAuthConfig{
			Mode:      "oauth",
			IssuerURL: "https://panda-proxy.ethpandaops.io",
			ClientID:  "panda",
		}

		changed, err := reconcileProxyAuth(path, legacy)
		require.NoError(t, err)
		assert.True(t, changed)

		data, err := os.ReadFile(path)
		require.NoError(t, err)

		var parsed map[string]any
		require.NoError(t, yaml.Unmarshal(data, &parsed))

		auth := parsed["proxy"].(map[string]any)["auth"].(map[string]any)
		_, hasMode := auth["mode"]
		assert.False(t, hasMode, "mode key should be removed for the oauth default")
	})

	t.Run("leaves config without a proxy.auth block untouched", func(t *testing.T) {
		t.Parallel()

		const noAuth = "server:\n  base_url: \"http://localhost:2480\"\nproxy:\n  url: \"https://p.example.com\"\n"

		path := filepath.Join(t.TempDir(), "config.yaml")
		require.NoError(t, os.WriteFile(path, []byte(noAuth), 0o644))

		changed, err := reconcileProxyAuth(path, authentik)
		require.NoError(t, err)
		assert.False(t, changed)

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, noAuth, string(data))
	})
}
