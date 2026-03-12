package cli

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	authclientpkg "github.com/ethpandaops/panda/pkg/auth/client"
	authstorepkg "github.com/ethpandaops/panda/pkg/auth/store"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveAuthTargetPrefersExplicitOverrides(t *testing.T) {
	newCLIHarness(t, http.NewServeMux())

	authIssuerURL = "https://issuer.example"
	authClientID = ""
	authResource = ""

	target, err := resolveAuthTarget(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "https://issuer.example", target.issuerURL)
	assert.Equal(t, defaultProxyAuthClientID, target.clientID)
	assert.Equal(t, "https://issuer.example", target.resource)
	assert.True(t, target.enabled)
}

func TestResolveAuthTargetFromConfigFallsBackToProxyURL(t *testing.T) {
	newCLIHarness(t, http.NewServeMux())

	require.NoError(t, os.WriteFile(cfgFile, []byte(`
server:
  url: http://server.example
proxy:
  url: https://proxy.example/
  auth:
    client_id: panda-cli
sandbox:
  image: sandbox:test
`), 0o600))

	target := resolveAuthTargetFromConfig()
	require.NotNil(t, target)
	assert.Equal(t, "https://proxy.example", target.issuerURL)
	assert.Equal(t, "panda-cli", target.clientID)
	assert.Equal(t, "https://proxy.example", target.resource)
	assert.True(t, target.enabled)
}

func TestRunAuthStatusReportsMissingAndExpiredCredentials(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	newCLIHarness(t, http.NewServeMux())

	require.NoError(t, os.WriteFile(cfgFile, []byte(`
server:
  url: http://server.example
proxy:
  url: https://proxy.example
  auth:
    issuer_url: https://issuer.example
    client_id: panda-cli
sandbox:
  image: sandbox:test
`), 0o600))

	target := resolveAuthTargetFromConfig()
	require.NotNil(t, target)

	store := authstorepkg.New(logrus.New(), authstorepkg.Config{
		IssuerURL: target.issuerURL,
		ClientID:  target.clientID,
		Resource:  target.resource,
	})
	require.NoError(t, store.Clear())

	stdout, _ := captureOutput(t, func() {
		require.NoError(t, runAuthStatus(authStatusCmd, nil))
	})
	assert.Contains(t, stdout, "Status: Not authenticated")

	require.NoError(t, store.Save(&authclientpkg.Tokens{
		AccessToken: "expired-token",
		TokenType:   "Bearer",
		ExpiresAt:   time.Now().Add(-time.Hour),
	}))

	stdout, _ = captureOutput(t, func() {
		require.NoError(t, runAuthStatus(authStatusCmd, nil))
	})
	assert.Contains(t, stdout, "Issuer: https://issuer.example")
	assert.Contains(t, stdout, "Client ID: panda-cli")
	assert.Contains(t, stdout, "Status: Expired")
}
