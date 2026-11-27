package proxy

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testAppKeyPEM(t *testing.T) string {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	return string(pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}))
}

func githubTestConfig(gh *GitHubAPIConfig) ServerConfig {
	return ServerConfig{
		Prometheus: []PrometheusInstanceConfig{{
			BaseDatasourceConfig: BaseDatasourceConfig{Name: "prom"},
			URL:                  "http://prometheus:9090",
		}},
		GitHub: gh,
	}
}

func TestValidateGitHubConfig(t *testing.T) {
	keyPEM := testAppKeyPEM(t)

	keyFile := filepath.Join(t.TempDir(), "app.pem")
	require.NoError(t, os.WriteFile(keyFile, []byte(keyPEM), 0o600))

	tests := map[string]struct {
		github  *GitHubAPIConfig
		wantErr string
	}{
		"token only": {
			github: &GitHubAPIConfig{Token: "ghp_test"},
		},
		"empty app block is ignored": {
			github: &GitHubAPIConfig{Token: "ghp_test", App: &GitHubAppAuthConfig{}},
		},
		"app with inline PEM": {
			github: &GitHubAPIConfig{App: &GitHubAppAuthConfig{AppID: "1", InstallationID: "2", PrivateKey: keyPEM}},
		},
		"app with base64 PEM": {
			github: &GitHubAPIConfig{App: &GitHubAppAuthConfig{
				AppID: "1", InstallationID: "2", PrivateKey: base64.StdEncoding.EncodeToString([]byte(keyPEM)),
			}},
		},
		"app with key file": {
			github: &GitHubAPIConfig{App: &GitHubAppAuthConfig{AppID: "1", InstallationID: "2", PrivateKeyFile: keyFile}},
		},
		"app missing app_id": {
			github:  &GitHubAPIConfig{App: &GitHubAppAuthConfig{InstallationID: "2", PrivateKey: keyPEM}},
			wantErr: "github.app.app_id",
		},
		"app missing installation_id": {
			github:  &GitHubAPIConfig{App: &GitHubAppAuthConfig{AppID: "1", PrivateKey: keyPEM}},
			wantErr: "github.app.installation_id",
		},
		"app missing key": {
			github:  &GitHubAPIConfig{App: &GitHubAppAuthConfig{AppID: "1", InstallationID: "2"}},
			wantErr: "github.app.private_key or github.app.private_key_file is required",
		},
		"app with both key sources": {
			github: &GitHubAPIConfig{App: &GitHubAppAuthConfig{
				AppID: "1", InstallationID: "2", PrivateKey: keyPEM, PrivateKeyFile: keyFile,
			}},
			wantErr: "mutually exclusive",
		},
		"app with unparsable key": {
			github: &GitHubAPIConfig{App: &GitHubAppAuthConfig{
				AppID: "1", InstallationID: "2", PrivateKey: "-----BEGIN RSA PRIVATE KEY-----\nbm9wZQ==\n-----END RSA PRIVATE KEY-----",
			}},
			wantErr: "parsing github.app private key",
		},
		"app with missing key file": {
			github: &GitHubAPIConfig{App: &GitHubAppAuthConfig{
				AppID: "1", InstallationID: "2", PrivateKeyFile: filepath.Join(t.TempDir(), "missing.pem"),
			}},
			wantErr: "reading github.app.private_key_file",
		},
		"app validated even when token is set": {
			github:  &GitHubAPIConfig{Token: "ghp_test", App: &GitHubAppAuthConfig{AppID: "1"}},
			wantErr: "github.app.installation_id",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := githubTestConfig(tc.github)
			err := cfg.Validate()

			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestLoadServerConfigGitHubAppFromEnv(t *testing.T) {
	keyPEM := testAppKeyPEM(t)

	t.Setenv("TEST_GITHUB_APP_ID", "5")
	t.Setenv("TEST_GITHUB_APP_INSTALLATION_ID", "6")
	t.Setenv("TEST_GITHUB_APP_PRIVATE_KEY", base64.StdEncoding.EncodeToString([]byte(keyPEM)))

	path := filepath.Join(t.TempDir(), "proxy-config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
prometheus:
  - name: prom
    url: http://prometheus:9090
github:
  app:
    app_id: ${TEST_GITHUB_APP_ID}
    installation_id: "${TEST_GITHUB_APP_INSTALLATION_ID}"
    private_key: "${TEST_GITHUB_APP_PRIVATE_KEY}"
  allowed_orgs:
    - ethpandaops
`), 0o600))

	cfg, err := LoadServerConfig(path)
	require.NoError(t, err)
	require.NotNil(t, cfg.GitHub)
	require.True(t, cfg.GitHub.App.IsSet())

	appCfg, err := cfg.GitHub.App.ToHandlerConfig()
	require.NoError(t, err)
	assert.Equal(t, int64(5), appCfg.AppID)
	assert.Equal(t, int64(6), appCfg.InstallationID)
	assert.NotNil(t, appCfg.PrivateKey)
}
