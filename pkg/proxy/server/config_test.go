package proxyserver

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	simpleauth "github.com/ethpandaops/panda/pkg/auth"
	"github.com/ethpandaops/panda/pkg/configutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServerConfigApplyDefaultsAndValidate(t *testing.T) {
	t.Parallel()

	cfg := &ServerConfig{
		ClickHouse: []ClickHouseDatasourceConfig{{
			Name:     "xatu",
			Host:     "clickhouse.example",
			Secure:   true,
			Username: "user",
			Password: "pass",
		}},
	}
	cfg.ApplyDefaults()

	assert.Equal(t, ":18081", cfg.Server.ListenAddr)
	assert.Equal(t, 30*time.Second, cfg.Server.ReadTimeout)
	assert.Equal(t, 5*time.Minute, cfg.Server.WriteTimeout)
	assert.Equal(t, 60*time.Second, cfg.Server.IdleTimeout)
	assert.Equal(t, AuthModeNone, cfg.Auth.Mode)
	assert.Equal(t, time.Hour, cfg.Auth.AccessTokenTTL)
	assert.Equal(t, 60, cfg.RateLimiting.RequestsPerMinute)
	assert.Equal(t, 10, cfg.RateLimiting.BurstSize)
	assert.Equal(t, 500, cfg.Audit.MaxQueryLength)
	assert.Equal(t, 9090, cfg.Metrics.Port)
	assert.Equal(t, 8443, cfg.ClickHouse[0].Port)

	require.NoError(t, cfg.Validate())

	insecure := &ServerConfig{
		ClickHouse: []ClickHouseDatasourceConfig{{
			Name:     "xatu",
			Host:     "clickhouse.example",
			Username: "user",
			Password: "pass",
		}},
	}
	insecure.ApplyDefaults()
	assert.Equal(t, 8123, insecure.ClickHouse[0].Port)
}

func TestServerConfigValidateRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  ServerConfig
		want string
	}{
		{
			name: "oauth requires github config",
			cfg: ServerConfig{
				Auth: AuthConfig{Mode: AuthModeOAuth},
				ClickHouse: []ClickHouseDatasourceConfig{{
					Name: "xatu",
					Host: "clickhouse.example",
				}},
			},
			want: "auth.github is required",
		},
		{
			name: "oauth requires github client id",
			cfg: ServerConfig{
				Auth: AuthConfig{
					Mode:   AuthModeOAuth,
					GitHub: &simpleauth.GitHubConfig{},
				},
				ClickHouse: []ClickHouseDatasourceConfig{{Name: "xatu", Host: "clickhouse.example"}},
			},
			want: "auth.github.client_id is required",
		},
		{
			name: "oauth requires github client secret",
			cfg: ServerConfig{
				Auth: AuthConfig{
					Mode: AuthModeOAuth,
					GitHub: &simpleauth.GitHubConfig{
						ClientID: "github-client",
					},
				},
				ClickHouse: []ClickHouseDatasourceConfig{{Name: "xatu", Host: "clickhouse.example"}},
			},
			want: "auth.github.client_secret is required",
		},
		{
			name: "oauth requires token secret",
			cfg: ServerConfig{
				Auth: AuthConfig{
					Mode: AuthModeOAuth,
					GitHub: &simpleauth.GitHubConfig{
						ClientID:     "github-client",
						ClientSecret: "github-secret",
					},
				},
				ClickHouse: []ClickHouseDatasourceConfig{{Name: "xatu", Host: "clickhouse.example"}},
			},
			want: "auth.tokens.secret_key is required",
		},
		{
			name: "requires a datasource",
			cfg:  ServerConfig{},
			want: "at least one datasource",
		},
		{
			name: "clickhouse name required",
			cfg: ServerConfig{
				ClickHouse: []ClickHouseDatasourceConfig{{Host: "clickhouse.example"}},
			},
			want: "clickhouse[0].name is required",
		},
		{
			name: "clickhouse host required",
			cfg: ServerConfig{
				ClickHouse: []ClickHouseDatasourceConfig{{Name: "xatu"}},
			},
			want: "clickhouse[0].host is required",
		},
		{
			name: "prometheus name required",
			cfg: ServerConfig{
				Prometheus: []PrometheusInstanceConfig{{URL: "https://prom.example"}},
			},
			want: "prometheus[0].name is required",
		},
		{
			name: "prometheus url required",
			cfg: ServerConfig{
				Prometheus: []PrometheusInstanceConfig{{Name: "prom"}},
			},
			want: "prometheus[0].url is required",
		},
		{
			name: "loki name required",
			cfg: ServerConfig{
				Loki: []LokiInstanceConfig{{URL: "https://logs.example"}},
			},
			want: "loki[0].name is required",
		},
		{
			name: "loki url required",
			cfg: ServerConfig{
				Loki: []LokiInstanceConfig{{Name: "logs"}},
			},
			want: "loki[0].url is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.cfg.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestLoadServerConfigAndSubstituteEnvVars(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PANDA_PROXY_CONFIG", "")
	t.Setenv("ETHPANDAOPS_PROXY_CONFIG", "")
	t.Setenv("CONFIG_PATH", "")
	t.Setenv("PROXY_PASSWORD", "secret-pass")

	path := filepath.Join(dir, "proxy-config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
# ${IGNORED}
clickhouse:
  - name: xatu
    host: clickhouse.example
    username: panda
    password: ${PROXY_PASSWORD}
prometheus:
  - name: prom
    url: ${PROM_URL:-https://prom.example}
`), 0o600))

	cfg, err := LoadServerConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "secret-pass", cfg.ClickHouse[0].Password)
	assert.Equal(t, "https://prom.example", cfg.Prometheus[0].URL)
	assert.Equal(t, ":18081", cfg.Server.ListenAddr)

	substituted, err := configutil.SubstituteEnvVars("# ${IGNORED}\npassword: ${PROXY_PASSWORD}\nmissing: ${MISSING:-fallback}")
	require.NoError(t, err)
	assert.Contains(t, substituted, "# ${IGNORED}")
	assert.Contains(t, substituted, "password: secret-pass")
	assert.Contains(t, substituted, "missing: fallback")
}
