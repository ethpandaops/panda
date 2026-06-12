package devnet

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/tools/clientcmd"
)

func TestEnsureCluster(t *testing.T) {
	t.Run("empty target is a no-op", func(t *testing.T) {
		t.Setenv("XDG_DATA_HOME", t.TempDir())

		var out bytes.Buffer
		require.NoError(t, EnsureCluster("", &out))
		assert.Empty(t, out.String())
	})

	t.Run("writes the setting when none exists", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_DATA_HOME", dir)

		var out bytes.Buffer
		require.NoError(t, EnsureCluster("bruno", &out))

		data, err := os.ReadFile(filepath.Join(dir, "kurtosis", "cluster-setting"))
		require.NoError(t, err)
		assert.Equal(t, "bruno", string(data))
		assert.Contains(t, out.String(), "Set Kurtosis cluster")
	})

	t.Run("accepts a matching setting", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_DATA_HOME", dir)
		require.NoError(t, EnsureCluster("bruno", &bytes.Buffer{}))

		var out bytes.Buffer
		require.NoError(t, EnsureCluster("bruno", &out))
		assert.Contains(t, out.String(), "Kurtosis cluster: bruno")
	})

	t.Run("refuses to switch a different active cluster", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_DATA_HOME", dir)
		require.NoError(t, EnsureCluster("docker", &bytes.Buffer{}))

		err := EnsureCluster("bruno", &bytes.Buffer{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "kurtosis cluster set bruno")
	})
}

func TestEnsureKubeContext(t *testing.T) {
	writeKubeconfig := func(t *testing.T, current string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "kubeconfig")
		content := `apiVersion: v1
kind: Config
current-context: ` + current + `
clusters:
- name: c-a
  cluster: {server: https://a.example}
- name: c-b
  cluster: {server: https://b.example}
contexts:
- name: ctx-a
  context: {cluster: c-a, user: u}
- name: ctx-b
  context: {cluster: c-b, user: u}
users:
- name: u
  user: {token: t}
`
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

		return path
	}

	currentContext := func(t *testing.T, path string) string {
		t.Helper()
		cfg, err := clientcmd.LoadFromFile(path)
		require.NoError(t, err)

		return cfg.CurrentContext
	}

	t.Run("empty context is a no-op", func(t *testing.T) {
		path := writeKubeconfig(t, "ctx-a")
		t.Setenv("KUBECONFIG", path)

		require.NoError(t, EnsureKubeContext("", &bytes.Buffer{}))
		assert.Equal(t, "ctx-a", currentContext(t, path))
	})

	t.Run("switches the current context", func(t *testing.T) {
		path := writeKubeconfig(t, "ctx-a")
		t.Setenv("KUBECONFIG", path)

		var out bytes.Buffer
		require.NoError(t, EnsureKubeContext("ctx-b", &out))
		assert.Equal(t, "ctx-b", currentContext(t, path))
		assert.Contains(t, out.String(), "ctx-b")
	})

	t.Run("already-current context is a quiet no-op", func(t *testing.T) {
		path := writeKubeconfig(t, "ctx-b")
		t.Setenv("KUBECONFIG", path)

		var out bytes.Buffer
		require.NoError(t, EnsureKubeContext("ctx-b", &out))
		assert.Equal(t, "ctx-b", currentContext(t, path))
		assert.Empty(t, out.String())
	})

	t.Run("errors on an unknown context", func(t *testing.T) {
		path := writeKubeconfig(t, "ctx-a")
		t.Setenv("KUBECONFIG", path)

		err := EnsureKubeContext("ctx-missing", &bytes.Buffer{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no context")
	})
}
