package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadPromotesSingularProxyToProxies(t *testing.T) {
	t.Parallel()

	path := writeConfig(t, `
server:
  base_url: "http://localhost:2480"
sandbox:
  image: "test-image:latest"
proxy:
  url: "http://hosted.example:18081/"
  auth:
    mode: "oidc"
    issuer_url: "https://issuer.example"
    client_id: "panda"
`)

	cfg, err := Load(path)
	require.NoError(t, err)

	require.Len(t, cfg.Proxies, 1)
	assert.Equal(t, "primary", cfg.Proxies[0].Name)
	assert.Equal(t, "http://hosted.example:18081", cfg.Proxies[0].URL)
	require.NotNil(t, cfg.Proxies[0].Auth)
	assert.Equal(t, "https://issuer.example", cfg.Proxies[0].Auth.IssuerURL)

	assert.Equal(t, cfg.Proxies[0], cfg.Proxy)
}

func TestLoadProxiesListSetsPrimaryCompatibilityProxy(t *testing.T) {
	t.Parallel()

	path := writeConfig(t, `
server:
  base_url: "http://localhost:2480"
sandbox:
  image: "test-image:latest"
proxies:
  - name: hosted
    url: "https://proxy.example/"
    auth:
      mode: "oidc"
      issuer_url: "https://issuer.example"
      client_id: "panda"
  - name: lab
    url: "http://lab.example:18081"
`)

	cfg, err := Load(path)
	require.NoError(t, err)

	require.Len(t, cfg.Proxies, 2)
	assert.Equal(t, "hosted", cfg.Proxies[0].Name)
	assert.Equal(t, "https://proxy.example", cfg.Proxies[0].URL)
	assert.Equal(t, "lab", cfg.Proxies[1].Name)
	assert.Equal(t, "http://lab.example:18081", cfg.Proxies[1].URL)
	assert.Equal(t, cfg.Proxies[0], cfg.Proxy)
}

func TestLoadRejectsSingularAndPluralProxyConfig(t *testing.T) {
	t.Parallel()

	path := writeConfig(t, `
server:
  base_url: "http://localhost:2480"
sandbox:
  image: "test-image:latest"
proxy:
  url: "http://hosted.example:18081"
proxies:
  - name: hosted
    url: "https://proxy.example"
`)

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `cannot set both "proxy" and "proxies"; use one or the other`)
}

func TestLoadClientPromotesSingularProxyToProxies(t *testing.T) {
	t.Parallel()

	path := writeConfig(t, `
server:
  url: "http://localhost:2480"
proxy:
  url: "http://hosted.example:18081/"
  auth:
    mode: "oidc"
    issuer_url: "https://issuer.example"
    client_id: "panda"
`)

	cfg, err := LoadClient(path)
	require.NoError(t, err)

	require.Len(t, cfg.Proxies, 1)
	assert.Equal(t, "primary", cfg.Proxies[0].Name)
	assert.Equal(t, "http://hosted.example:18081", cfg.Proxies[0].URL)
	assert.Equal(t, cfg.Proxies[0], cfg.Proxy)
}

func TestLoadClientProxiesListSetsPrimaryCompatibilityProxy(t *testing.T) {
	t.Parallel()

	path := writeConfig(t, `
server:
  url: "http://localhost:2480"
proxies:
  - name: hosted
    url: "https://proxy.example/"
    auth:
      mode: "oidc"
      issuer_url: "https://issuer.example"
      client_id: "panda"
`)

	cfg, err := LoadClient(path)
	require.NoError(t, err)

	require.Len(t, cfg.Proxies, 1)
	assert.Equal(t, "hosted", cfg.Proxy.Name)
	assert.Equal(t, "https://proxy.example", cfg.Proxy.URL)
	require.NotNil(t, cfg.Proxy.Auth)
	assert.Equal(t, "panda", cfg.Proxy.Auth.ClientID)
}

func TestLoadClientRejectsSingularAndPluralProxyConfig(t *testing.T) {
	t.Parallel()

	path := writeConfig(t, `
server:
  url: "http://localhost:2480"
proxy:
  url: "http://hosted.example:18081"
proxies:
  - name: hosted
    url: "https://proxy.example"
`)

	_, err := LoadClient(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `cannot set both "proxy" and "proxies"; use one or the other`)
}

func TestLoadLocalProxyDefaults(t *testing.T) {
	t.Parallel()

	path := writeConfig(t, `
server:
  base_url: "http://localhost:2480"
sandbox:
  image: "test-image:latest"
proxy:
  url: "http://hosted.example:18081"
`)

	cfg, err := Load(path)
	require.NoError(t, err)

	require.NotNil(t, cfg.LocalProxy.Enabled)
	assert.True(t, cfg.LocalProxy.IsEnabled())
	require.Len(t, cfg.LocalProxy.ClickHouse, 1)

	clickhouse := cfg.LocalProxy.ClickHouse[0]
	assert.Equal(t, "local-kurtosis", clickhouse.Name)
	assert.Equal(t, defaultLocalProxyClickHouseDesc, clickhouse.Description)
	assert.Equal(t, "host.docker.internal", clickhouse.Host)
	assert.Equal(t, 18123, clickhouse.Port)
	assert.Equal(t, "otel", clickhouse.Database)
	assert.True(t, clickhouse.Autodiscover)
	assert.Equal(t, 10*time.Second, clickhouse.AutodiscoverInterval)
}

func TestLoadLocalProxyClickHouseOverride(t *testing.T) {
	t.Parallel()

	path := writeConfig(t, `
server:
  base_url: "http://localhost:2480"
sandbox:
  image: "test-image:latest"
proxy:
  url: "http://hosted.example:18081"
local_proxy:
  enabled: false
  clickhouse:
    - name: "local-kurtosis"
      description: "Custom local datasource"
      host: "localhost"
      port: 18123
      database: "otel"
      username: "default"
      password: "secret"
      secure: true
      autodiscover: true
      autodiscover_interval: 15s
`)

	cfg, err := Load(path)
	require.NoError(t, err)

	assert.False(t, cfg.LocalProxy.IsEnabled())
	require.Len(t, cfg.LocalProxy.ClickHouse, 1)

	clickhouse := cfg.LocalProxy.ClickHouse[0]
	assert.Equal(t, "local-kurtosis", clickhouse.Name)
	assert.Equal(t, "Custom local datasource", clickhouse.Description)
	assert.Equal(t, "localhost", clickhouse.Host)
	assert.Equal(t, 18123, clickhouse.Port)
	assert.Equal(t, "otel", clickhouse.Database)
	assert.Equal(t, "default", clickhouse.Username)
	assert.Equal(t, "secret", clickhouse.Password)
	assert.True(t, clickhouse.Secure)
	assert.True(t, clickhouse.Autodiscover)
	assert.Equal(t, 15*time.Second, clickhouse.AutodiscoverInterval)
}

func TestLoadRejectsDuplicateProxyNames(t *testing.T) {
	t.Parallel()

	path := writeConfig(t, `
server:
  base_url: "http://localhost:2480"
sandbox:
  image: "test-image:latest"
proxies:
  - name: hosted
    url: "https://one.example"
  - name: hosted
    url: "https://two.example"
`)

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `duplicates "hosted"`)
}

func TestLoadRejectsInvalidLocalProxyClickHouse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		block   string
		wantErr string
	}{
		{
			name: "missing host",
			block: `
    - name: "local-kurtosis"
      database: "otel"
`,
			wantErr: `local_proxy.clickhouse[0].host is required`,
		},
		{
			name: "negative interval",
			block: `
    - name: "local-kurtosis"
      host: "localhost"
      database: "otel"
      autodiscover: true
      autodiscover_interval: -1s
`,
			wantErr: "local_proxy.clickhouse[0].autodiscover_interval cannot be negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := writeConfig(t, `
server:
  base_url: "http://localhost:2480"
sandbox:
  image: "test-image:latest"
proxy:
  url: "http://hosted.example:18081"
local_proxy:
  clickhouse:
`+tt.block)

			_, err := Load(path)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	return path
}
