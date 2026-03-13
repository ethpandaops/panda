package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestBuildConfigTemplate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		proxyURL     string
		sandboxImage string
	}{
		{
			name:         "default values",
			proxyURL:     defaultProxyURL,
			sandboxImage: defaultSandboxImage,
		},
		{
			name:         "custom values",
			proxyURL:     "https://custom-proxy.example.com",
			sandboxImage: "ghcr.io/myorg/sandbox:v1.2.3",
		},
		{
			name:         "localhost proxy",
			proxyURL:     "http://localhost:18081",
			sandboxImage: "local-sandbox:dev",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := buildConfigTemplate(tt.proxyURL, tt.sandboxImage)

			// Parse the generated YAML.
			var parsed map[string]any
			err := yaml.Unmarshal([]byte(result), &parsed)
			require.NoError(t, err, "generated config must be valid YAML")

			// Verify server section.
			server, ok := parsed["server"].(map[string]any)
			require.True(t, ok, "config must have a server section")
			assert.Equal(t, "0.0.0.0", server["host"])
			assert.Equal(t, 2480, server["port"])
			assert.Equal(t, "http://localhost:2480", server["base_url"])
			assert.Equal(t, "http://panda-server:2480", server["sandbox_url"])

			// Verify sandbox section.
			sandbox, ok := parsed["sandbox"].(map[string]any)
			require.True(t, ok, "config must have a sandbox section")
			assert.Equal(t, tt.sandboxImage, sandbox["image"])
			assert.Equal(t, "ethpandaops-panda-internal", sandbox["network"])
			assert.Equal(t, "/tmp/ethpandaops-panda-sandbox", sandbox["host_shared_path"])

			// Verify proxy section.
			proxy, ok := parsed["proxy"].(map[string]any)
			require.True(t, ok, "config must have a proxy section")
			assert.Equal(t, tt.proxyURL, proxy["url"])

			auth, ok := proxy["auth"].(map[string]any)
			require.True(t, ok, "proxy must have an auth block")
			assert.Equal(t, tt.proxyURL, auth["issuer_url"])
			assert.Equal(t, defaultProxyClientID, auth["client_id"])
		})
	}
}

func TestBuildComposeTemplate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		serverImage string
		configDir   string
	}{
		{
			name:        "default values",
			serverImage: defaultServerImage,
			configDir:   "/home/user/.config/panda",
		},
		{
			name:        "custom image and dir",
			serverImage: "ghcr.io/myorg/server:v2.0.0",
			configDir:   "/opt/panda/config",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := buildComposeTemplate(tt.serverImage, tt.configDir)

			// Parse the generated YAML.
			var parsed map[string]any
			err := yaml.Unmarshal([]byte(result), &parsed)
			require.NoError(t, err, "generated compose file must be valid YAML")

			// Verify services section.
			services, ok := parsed["services"].(map[string]any)
			require.True(t, ok, "compose file must have a services section")

			svc, ok := services["panda-server"].(map[string]any)
			require.True(t, ok, "compose file must have a panda-server service")

			assert.Equal(t, tt.serverImage, svc["image"])
			assert.Equal(t, "panda-server", svc["container_name"])

			// Verify port mapping.
			ports, ok := svc["ports"].([]any)
			require.True(t, ok, "service must have ports")
			require.Len(t, ports, 1)
			assert.Equal(t, "127.0.0.1:2480:2480", ports[0])

			// Verify volumes include config mount with the given dir.
			volumes, ok := svc["volumes"].([]any)
			require.True(t, ok, "service must have volumes")

			configMountFound := false
			expectedMount := tt.configDir + "/config.yaml:/app/config.yaml:ro"

			for _, v := range volumes {
				if v == expectedMount {
					configMountFound = true

					break
				}
			}

			assert.True(t, configMountFound,
				"volumes should contain config mount %q, got %v",
				expectedMount, volumes)

			// Verify command starts with the full binary name.
			// Bare subcommands like ["serve", ...] break the docker-entrypoint.sh
			// which needs ["panda-server", "serve", ...].
			cmdList, ok := svc["command"].([]any)
			require.True(t, ok, "service must have a command list")
			require.NotEmpty(t, cmdList, "command list must not be empty")
			assert.Equal(t, "panda-server", cmdList[0],
				"command must start with 'panda-server', not a bare subcommand")

			// Verify networks section.
			networks, ok := parsed["networks"].(map[string]any)
			require.True(t, ok, "compose file must have a networks section")

			pandaNet, ok := networks["panda-internal"].(map[string]any)
			require.True(t, ok, "networks must include panda-internal")
			assert.Equal(t, "ethpandaops-panda-internal", pandaNet["name"])
			assert.Equal(t, "bridge", pandaNet["driver"])
		})
	}
}

func TestWriteConfigFile(t *testing.T) {
	t.Parallel()

	t.Run("creates new file when none exists", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		path := filepath.Join(dir, "config.yaml")
		content := "key: value\n"

		created, err := writeConfigFile(path, content, false)
		require.NoError(t, err)
		assert.Equal(t, 1, created, "should return 1 when file is created")

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, content, string(data))
	})

	t.Run("returns 0 when file exists and force is false", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		path := filepath.Join(dir, "config.yaml")
		original := "original: content\n"

		require.NoError(t, os.WriteFile(path, []byte(original), 0o644))

		created, err := writeConfigFile(path, "new: content\n", false)
		require.NoError(t, err)
		assert.Equal(t, 0, created, "should return 0 when file exists and force=false")

		// Verify original content is preserved.
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, original, string(data))
	})

	t.Run("overwrites when force is true", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		path := filepath.Join(dir, "config.yaml")
		original := "original: content\n"
		updated := "updated: content\n"

		require.NoError(t, os.WriteFile(path, []byte(original), 0o644))

		created, err := writeConfigFile(path, updated, true)
		require.NoError(t, err)
		assert.Equal(t, 1, created, "should return 1 when force=true overwrites")

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, updated, string(data))
	})

	t.Run("returns error for invalid path", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "nonexistent", "subdir", "config.yaml")

		_, err := writeConfigFile(path, "content\n", false)
		require.Error(t, err)
	})
}
