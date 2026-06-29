package server

import (
	"context"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	authclient "github.com/ethpandaops/panda/pkg/auth/client"
	authstore "github.com/ethpandaops/panda/pkg/auth/store"
	"github.com/ethpandaops/panda/pkg/config"
	"github.com/ethpandaops/panda/pkg/serverapi"
)

// TestCredentialPathMatchesProxyTokenSource guards the highest-risk invariant:
// the server's credential controller must resolve the exact same credential
// file as the proxy client's token source, for every auth mode — otherwise a
// login writes a file the proxy never reads. Both sides now go through the
// shared ProxyConfig resolvers, so this asserts they stay in lockstep.
func TestCredentialPathMatchesProxyTokenSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		pc   config.ProxyConfig
	}{
		{
			name: "oauth issuer differs from url",
			pc: config.ProxyConfig{URL: "https://proxy.example", Auth: &config.ProxyAuthConfig{
				Mode: "oauth", IssuerURL: "https://issuer.example", ClientID: "panda",
			}},
		},
		{
			name: "oauth no explicit issuer",
			pc: config.ProxyConfig{URL: "https://proxy.example", Auth: &config.ProxyAuthConfig{
				Mode: "oauth", ClientID: "panda",
			}},
		},
		{
			name: "oidc external issuer",
			pc: config.ProxyConfig{URL: "https://proxy.example", Auth: &config.ProxyAuthConfig{
				Mode: "oidc", IssuerURL: "https://issuer.example", ClientID: "panda",
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// The credential file the proxy client's token source keys on.
			proxyPath := authstore.New(logrus.New(), authstore.Config{
				IssuerURL: tt.pc.ResolvedAuthIssuerURL(),
				ClientID:  tt.pc.Auth.ClientID,
				Resource:  tt.pc.ResolvedAuthResource(),
			}).Path()

			// The credential file the server's controller operates on.
			ctrl := newCredentialController(logrus.New(), buildProxyAuthMetadata(&config.Config{Proxy: tt.pc}))
			require.NotNil(t, ctrl)
			require.Equal(t, proxyPath, ctrl.Status().CredentialsPath)
		})
	}
}

// TestRunLoginDropsSupersededAttempt verifies a poll that returns after its
// login was superseded (or logged out) neither persists tokens nor mutates the
// current login state.
func TestRunLoginDropsSupersededAttempt(t *testing.T) {
	t.Parallel()

	fake := &fakeAuthClient{
		pollResp: &authclient.Tokens{AccessToken: "at", RefreshToken: "rt", ExpiresAt: time.Now().Add(time.Hour)},
	}
	ctrl, _ := newTestController(t, fake)

	// A newer login is already current; the old attempt below must be dropped.
	current := &pendingLogin{state: serverapi.AuthLoginPending, cancel: func() {}}
	ctrl.pending = current

	old := &pendingLogin{state: serverapi.AuthLoginPending, cancel: func() {}}
	ctrl.runLogin(context.Background(), fake, &authclient.DeviceAuth{DeviceCode: "d"}, old)

	require.False(t, ctrl.Status().Authenticated, "superseded attempt must not persist tokens")
	require.Equal(t, serverapi.AuthLoginPending, old.state, "superseded attempt must not record a result")
	require.Same(t, current, ctrl.pending, "current login must be untouched")
}
