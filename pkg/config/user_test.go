package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeepMerge(t *testing.T) {
	tests := []struct {
		name     string
		base     map[string]any
		overlay  map[string]any
		expected map[string]any
	}{
		{
			name:     "empty overlay returns base",
			base:     map[string]any{"a": 1, "b": "hello"},
			overlay:  map[string]any{},
			expected: map[string]any{"a": 1, "b": "hello"},
		},
		{
			name:     "overlay adds new keys",
			base:     map[string]any{"a": 1},
			overlay:  map[string]any{"b": 2},
			expected: map[string]any{"a": 1, "b": 2},
		},
		{
			name:     "overlay overrides leaf values",
			base:     map[string]any{"a": 1, "b": "old"},
			overlay:  map[string]any{"b": "new"},
			expected: map[string]any{"a": 1, "b": "new"},
		},
		{
			name: "nested maps merge recursively",
			base: map[string]any{
				"sandbox": map[string]any{
					"timeout":      60,
					"memory_limit": "2g",
					"sessions": map[string]any{
						"max_sessions": 10,
						"ttl":          "30m",
					},
				},
			},
			overlay: map[string]any{
				"sandbox": map[string]any{
					"timeout": 120,
					"sessions": map[string]any{
						"max_sessions": 20,
					},
				},
			},
			expected: map[string]any{
				"sandbox": map[string]any{
					"timeout":      120,
					"memory_limit": "2g",
					"sessions": map[string]any{
						"max_sessions": 20,
						"ttl":          "30m",
					},
				},
			},
		},
		{
			name:     "overlay replaces map with scalar",
			base:     map[string]any{"a": map[string]any{"b": 1}},
			overlay:  map[string]any{"a": "scalar"},
			expected: map[string]any{"a": "scalar"},
		},
		{
			name:     "does not mutate base",
			base:     map[string]any{"a": 1},
			overlay:  map[string]any{"a": 2},
			expected: map[string]any{"a": 2},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := DeepMerge(tt.base, tt.overlay)
			assert.Equal(t, tt.expected, result)

			// Verify base was not mutated for the mutation test.
			if tt.name == "does not mutate base" {
				assert.Equal(t, 1, tt.base["a"], "base map should not be mutated")
			}
		})
	}
}

func TestSetNestedValue(t *testing.T) {
	m := make(map[string]any, 4)

	setNestedValue(m, []string{"sandbox", "timeout"}, 120)
	setNestedValue(m, []string{"sandbox", "sessions", "max_sessions"}, 20)

	sandbox, ok := m["sandbox"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, 120, sandbox["timeout"])

	sessions, ok := sandbox["sessions"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, 20, sessions["max_sessions"])
}

func TestBuildOverrideMap(t *testing.T) {
	fields := []OverrideField{
		{Path: "sandbox.timeout", Value: 120, Default: 60},              // changed
		{Path: "sandbox.memory_limit", Value: "2g", Default: "2g"},      // unchanged
		{Path: "sandbox.sessions.max_sessions", Value: 20, Default: 10}, // changed
	}

	result := BuildOverrideMap(fields)

	sandbox, ok := result["sandbox"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, 120, sandbox["timeout"])
	assert.NotContains(t, sandbox, "memory_limit", "unchanged field should be omitted")

	sessions, ok := sandbox["sessions"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, 20, sessions["max_sessions"])
}

func TestSaveAndLoadUserConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.user.yaml")

	values := map[string]any{
		"sandbox": map[string]any{
			"timeout": 120,
			"sessions": map[string]any{
				"max_sessions": 50,
				"ttl":          "1h",
			},
		},
	}

	err := SaveUserConfig(path, values)
	require.NoError(t, err)

	// Verify file has the header comment.
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "# User configuration overrides")

	// Load it back.
	loaded, err := LoadUserConfigMap(path)
	require.NoError(t, err)

	sandbox, ok := loaded["sandbox"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, 120, sandbox["timeout"])

	sessions, ok := sandbox["sessions"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, 50, sessions["max_sessions"])
	assert.Equal(t, "1h", sessions["ttl"])
}

func TestLoadUserConfigMapMissing(t *testing.T) {
	m, err := LoadUserConfigMap("/nonexistent/path/config.user.yaml")
	require.NoError(t, err)
	assert.Empty(t, m)
}

func TestLoadWithUserOverrides(t *testing.T) {
	dir := t.TempDir()

	// Write a base config.
	baseConfig := `
server:
  host: "0.0.0.0"
  port: 2480
  base_url: "http://localhost:2480"

sandbox:
  image: "test-image:latest"
  timeout: 60
  memory_limit: "2g"

proxy:
  url: "http://localhost:18081"
`
	basePath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(basePath, []byte(baseConfig), 0o644))

	t.Run("no user config returns base", func(t *testing.T) {
		cfg, err := LoadWithUserOverrides(basePath)
		require.NoError(t, err)
		assert.Equal(t, 60, cfg.Sandbox.Timeout)
		assert.Equal(t, "2g", cfg.Sandbox.MemoryLimit)
	})

	t.Run("user config overrides specific fields", func(t *testing.T) {
		userConfig := `
sandbox:
  timeout: 300
  sessions:
    max_sessions: 50
    ttl: 2h
`
		userPath := filepath.Join(dir, "config.user.yaml")
		require.NoError(t, os.WriteFile(userPath, []byte(userConfig), 0o644))

		cfg, err := LoadWithUserOverrides(basePath)
		require.NoError(t, err)
		assert.Equal(t, 300, cfg.Sandbox.Timeout)
		assert.Equal(t, "2g", cfg.Sandbox.MemoryLimit) // not overridden
		assert.Equal(t, 50, cfg.Sandbox.Sessions.MaxSessions)

		// Clean up for other subtests.
		require.NoError(t, os.Remove(userPath))
	})

	t.Run("empty user config returns base", func(t *testing.T) {
		userPath := filepath.Join(dir, "config.user.yaml")
		require.NoError(t, os.WriteFile(userPath, []byte("# empty\n"), 0o644))

		cfg, err := LoadWithUserOverrides(basePath)
		require.NoError(t, err)
		assert.Equal(t, 60, cfg.Sandbox.Timeout)

		require.NoError(t, os.Remove(userPath))
	})
}

func TestUserConfigPath(t *testing.T) {
	path := UserConfigPath("/home/user/.config/panda/config.yaml")
	assert.Equal(t, "/home/user/.config/panda/config.user.yaml", path)
}
