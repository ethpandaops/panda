package devnet

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestInjectDockerCache(t *testing.T) {
	t.Run("injects into YAML args", func(t *testing.T) {
		in := "participants:\n  - el_type: geth\n"

		out, err := injectDockerCache(in, "docker.ethquokkaops.io")
		require.NoError(t, err)

		var got map[string]interface{}
		require.NoError(t, yaml.Unmarshal([]byte(out), &got))

		assert.Contains(t, got, "participants")
		cache, ok := got["docker_cache_params"].(map[string]interface{})
		require.True(t, ok, "docker_cache_params should be a map")
		assert.Equal(t, true, cache["enabled"])
		assert.Equal(t, "docker.ethquokkaops.io", cache["url"])
	})

	t.Run("injects into JSON args", func(t *testing.T) {
		out, err := injectDockerCache(`{"participants": []}`, "cache.example")
		require.NoError(t, err)

		var got map[string]interface{}
		require.NoError(t, yaml.Unmarshal([]byte(out), &got))

		cache, ok := got["docker_cache_params"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "cache.example", cache["url"])
	})

	t.Run("preserves an explicit docker_cache_params", func(t *testing.T) {
		in := "docker_cache_params:\n  enabled: true\n  url: mine.example\n"

		out, err := injectDockerCache(in, "docker.ethquokkaops.io")
		require.NoError(t, err)

		var got map[string]interface{}
		require.NoError(t, yaml.Unmarshal([]byte(out), &got))

		cache := got["docker_cache_params"].(map[string]interface{})
		assert.Equal(t, "mine.example", cache["url"], "user value must win")
	})

	t.Run("rejects malformed args", func(t *testing.T) {
		_, err := injectDockerCache("\t not: : valid", "cache.example")
		assert.Error(t, err)
	})
}
