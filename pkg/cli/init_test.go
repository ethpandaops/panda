package cli

import (
	"os"
	"path/filepath"
	"strings"
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

			authCfg := initAuthConfig{
				IssuerURL: "https://dex.example.com",
				ClientID:  "test-client",
			}
			result := buildConfigTemplate(tt.proxyURL, tt.sandboxImage, authCfg)

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
			assert.Equal(t, authCfg.IssuerURL, auth["issuer_url"])
			assert.Equal(t, authCfg.ClientID, auth["client_id"])
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

			extraHosts, ok := svc["extra_hosts"].([]any)
			require.True(t, ok, "service must have extra_hosts")
			require.Contains(t, extraHosts, "host.docker.internal:host-gateway")

			// Forced DNS servers break setups where public resolvers are
			// unreachable (issue #156); DNS must be left to Docker.
			assert.NotContains(t, svc, "dns",
				"compose template must not force container DNS")

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

			// The Docker socket must always be mounted to the in-container
			// path the sandbox backend dials, regardless of the host source.
			socketMountFound := false

			for _, v := range volumes {
				if s, ok := v.(string); ok && strings.HasSuffix(s, ":/var/run/docker.sock") {
					socketMountFound = true

					break
				}
			}

			assert.True(t, socketMountFound,
				"volumes must mount the Docker socket to /var/run/docker.sock, got %v",
				volumes)

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

func TestResolveDockerSocketPath(t *testing.T) {
	tests := []struct {
		name       string
		dockerHost string
		want       string
	}{
		{
			name:       "unset falls back to default",
			dockerHost: "",
			want:       "/var/run/docker.sock",
		},
		{
			name:       "rootless socket under XDG_RUNTIME_DIR",
			dockerHost: "unix:///run/user/1000/docker.sock",
			want:       "/run/user/1000/docker.sock",
		},
		{
			name:       "explicit rootful unix socket",
			dockerHost: "unix:///var/run/docker.sock",
			want:       "/var/run/docker.sock",
		},
		{
			name:       "tcp endpoint is not mountable, falls back to default",
			dockerHost: "tcp://127.0.0.1:2375",
			want:       "/var/run/docker.sock",
		},
		{
			name:       "ssh endpoint falls back to default",
			dockerHost: "ssh://user@remote",
			want:       "/var/run/docker.sock",
		},
		{
			name:       "empty unix path falls back to default",
			dockerHost: "unix://",
			want:       "/var/run/docker.sock",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// t.Setenv mutates process env, so this test cannot be parallel.
			t.Setenv("DOCKER_HOST", tt.dockerHost)

			assert.Equal(t, tt.want, resolveDockerSocketPath())
		})
	}
}

// TestBuildComposeTemplateRootlessSocket verifies the generated compose binds
// the rootless Docker socket from DOCKER_HOST rather than the rootful default
// (issue #168).
func TestBuildComposeTemplateRootlessSocket(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///run/user/1000/docker.sock")

	result := buildComposeTemplate(defaultServerImage, "/home/user/.config/panda")

	assert.Contains(t, result, "- /run/user/1000/docker.sock:/var/run/docker.sock",
		"compose must bind-mount the rootless socket from DOCKER_HOST")
}

func TestImageForVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		version     string
		wantServer  string
		wantSandbox string
	}{
		{
			name:        "release version",
			version:     "0.31.0",
			wantServer:  "ethpandaops/panda:server-0.31.0",
			wantSandbox: "ethpandaops/panda:sandbox-0.31.0",
		},
		{
			name:        "release version with v prefix",
			version:     "v0.31.0",
			wantServer:  "ethpandaops/panda:server-0.31.0",
			wantSandbox: "ethpandaops/panda:sandbox-0.31.0",
		},
		{
			name:        "pre-release keeps the suffix",
			version:     "v0.31.0-rc1",
			wantServer:  "ethpandaops/panda:server-0.31.0-rc1",
			wantSandbox: "ethpandaops/panda:sandbox-0.31.0-rc1",
		},
		{
			name:        "dev build falls back to latest",
			version:     "dev",
			wantServer:  defaultServerImage,
			wantSandbox: defaultSandboxImage,
		},
		{
			name:        "unknown falls back to latest",
			version:     "unknown",
			wantServer:  defaultServerImage,
			wantSandbox: defaultSandboxImage,
		},
		{
			name:        "empty falls back to latest",
			version:     "",
			wantServer:  defaultServerImage,
			wantSandbox: defaultSandboxImage,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.wantServer, serverImageForVersion(tt.version))
			assert.Equal(t, tt.wantSandbox, sandboxImageForVersion(tt.version))
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
