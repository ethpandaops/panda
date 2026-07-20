package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	authclient "github.com/ethpandaops/panda/pkg/auth/client"
	authstore "github.com/ethpandaops/panda/pkg/auth/store"
	"github.com/ethpandaops/panda/pkg/proxy"
	"github.com/ethpandaops/panda/pkg/serverapi"
)

// fakeAuthClient is an injectable authclient.Client for controller tests.
type fakeAuthClient struct {
	beginResp *authclient.DeviceAuth
	beginErr  error

	pollResp *authclient.Tokens
	pollErr  error
	pollGate chan struct{} // when non-nil, PollDeviceLogin blocks until closed

	beginCalls atomic.Int64
	pollCalls  atomic.Int64
}

func (f *fakeAuthClient) Login(_ context.Context) (*authclient.Tokens, error) {
	return nil, errors.New("unused")
}

func (f *fakeAuthClient) BeginDeviceLogin(_ context.Context) (*authclient.DeviceAuth, error) {
	f.beginCalls.Add(1)

	return f.beginResp, f.beginErr
}

func (f *fakeAuthClient) PollDeviceLogin(ctx context.Context, _ *authclient.DeviceAuth) (*authclient.Tokens, error) {
	f.pollCalls.Add(1)

	if f.pollGate != nil {
		select {
		case <-f.pollGate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	return f.pollResp, f.pollErr
}

func (f *fakeAuthClient) Refresh(_ context.Context, _ string) (*authclient.Tokens, error) {
	return nil, errors.New("unused")
}

func (f *fakeAuthClient) ClientCredentials(_ context.Context) (*authclient.Tokens, error) {
	return nil, errors.New("unused")
}

// newTestController builds a controller whose store is a real on-disk store at a
// temp path, so the full Save/Load/Clear path is exercised.
func newTestController(t *testing.T, fake authclient.Client) (*credentialController, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "creds.json")

	return &credentialController{
		log:    logrus.New(),
		target: credentialTarget{issuerURL: "https://issuer.example", clientID: "panda", enabled: true},
		newClient: func(credentialTarget) authclient.Client {
			return fake
		},
		newStore: func(_ credentialTarget, c authclient.Client) authstore.Store {
			return authstore.New(logrus.New(), authstore.Config{Path: path, AuthClient: c})
		},
	}, path
}

func seedTokens(t *testing.T, path string, tokens *authclient.Tokens) {
	t.Helper()
	require.NoError(t, authstore.New(logrus.New(), authstore.Config{Path: path}).Save(tokens))
}

func TestNewCredentialController(t *testing.T) {
	t.Parallel()

	require.Nil(t, newCredentialController(logrus.New(), nil, ""), "nil metadata yields nil controller")
	require.Nil(t, newCredentialController(logrus.New(), &serverapi.ProxyAuthMetadataResponse{}, ""),
		"disabled auth yields nil controller (e.g. client_credentials)")

	ctrl := newCredentialController(logrus.New(), &serverapi.ProxyAuthMetadataResponse{
		Enabled: true, IssuerURL: "https://i", ClientID: "panda", Resource: "https://r",
	}, "")
	require.NotNil(t, ctrl)
	require.Equal(t, "https://i", ctrl.target.issuerURL)
	require.Equal(t, "panda", ctrl.target.clientID)
	require.Equal(t, "https://r", ctrl.target.resource)
}

func TestCredentialControllerStatus(t *testing.T) {
	t.Parallel()

	now := time.Now()

	tests := []struct {
		name          string
		seed          *authclient.Tokens
		wantAuthed    bool
		wantExpired   bool
		wantRefresh   bool
		wantRefreshTS bool
	}{
		{
			name:       "no credentials",
			seed:       nil,
			wantAuthed: false,
		},
		{
			name:          "valid access with refresh",
			seed:          &authclient.Tokens{AccessToken: "a", RefreshToken: "r", ExpiresAt: now.Add(time.Hour), RefreshTokenIssuedAt: now},
			wantAuthed:    true,
			wantExpired:   false,
			wantRefresh:   true,
			wantRefreshTS: true,
		},
		{
			name:        "expired access but refresh present",
			seed:        &authclient.Tokens{AccessToken: "a", RefreshToken: "r", ExpiresAt: now.Add(-time.Minute)},
			wantAuthed:  true, // can still refresh
			wantExpired: true,
			wantRefresh: true,
		},
		{
			name:        "expired access no refresh",
			seed:        &authclient.Tokens{AccessToken: "a", ExpiresAt: now.Add(-time.Minute)},
			wantAuthed:  false,
			wantExpired: true,
			wantRefresh: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl, path := newTestController(t, &fakeAuthClient{})
			if tt.seed != nil {
				seedTokens(t, path, tt.seed)
			}

			st := ctrl.Status()

			require.True(t, st.Enabled)
			require.Equal(t, path, st.CredentialsPath)
			require.Equal(t, tt.wantAuthed, st.Authenticated)
			require.Equal(t, tt.wantExpired, st.Expired)
			require.Equal(t, tt.wantRefresh, st.RefreshTokenPresent)
			require.Equal(t, tt.wantRefreshTS, st.RefreshTokenIssuedAt != nil)
		})
	}
}

func TestCredentialControllerLoginSucceeds(t *testing.T) {
	t.Parallel()

	fake := &fakeAuthClient{
		beginResp: &authclient.DeviceAuth{
			DeviceCode: "secret-device-code", UserCode: "WXYZ-1234",
			VerificationURI: "https://issuer.example/device", ExpiresIn: 600, Interval: 5,
		},
		pollResp: &authclient.Tokens{AccessToken: "at", RefreshToken: "rt", ExpiresAt: time.Now().Add(time.Hour)},
	}

	ctrl, path := newTestController(t, fake)

	resp, err := ctrl.BeginLogin(context.Background())
	require.NoError(t, err)
	require.True(t, resp.Enabled)
	require.Equal(t, "WXYZ-1234", resp.UserCode)
	require.Equal(t, "https://issuer.example/device", resp.VerificationURI)

	// The opaque device code must never be surfaced to the caller. The response
	// type carries no such field; assert the secret does not appear in the JSON.
	raw, err := json.Marshal(resp)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "secret-device-code")

	require.Eventually(t, func() bool {
		return ctrl.LoginState().State == serverapi.AuthLoginAuthenticated
	}, 2*time.Second, 10*time.Millisecond)

	// Tokens were persisted to the same file Status reads.
	st := ctrl.Status()
	require.True(t, st.Authenticated)
	require.True(t, st.RefreshTokenPresent)

	stored, err := authstore.New(logrus.New(), authstore.Config{Path: path}).Load()
	require.NoError(t, err)
	require.Equal(t, "at", stored.AccessToken)
}

func TestCredentialControllerLoginBeginError(t *testing.T) {
	t.Parallel()

	fake := &fakeAuthClient{beginErr: errors.New("device endpoint unavailable")}
	ctrl, _ := newTestController(t, fake)

	_, err := ctrl.BeginLogin(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "device endpoint unavailable")
	require.Equal(t, serverapi.AuthLoginNone, ctrl.LoginState().State, "no pending login recorded on begin error")
}

func TestCredentialControllerLoginPollError(t *testing.T) {
	t.Parallel()

	fake := &fakeAuthClient{
		beginResp: &authclient.DeviceAuth{DeviceCode: "d", UserCode: "U", ExpiresIn: 600},
		pollErr:   errors.New("authorization was denied"),
	}
	ctrl, _ := newTestController(t, fake)

	_, err := ctrl.BeginLogin(context.Background())
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return ctrl.LoginState().State == serverapi.AuthLoginError
	}, 2*time.Second, 10*time.Millisecond)

	require.Contains(t, ctrl.LoginState().Error, "denied")
}

func TestCredentialControllerLoginSaveError(t *testing.T) {
	t.Parallel()

	// Point the store at a path whose parent is a regular file so MkdirAll (and
	// thus Save) fails, exercising the save-error branch.
	tmp := t.TempDir()
	blocker := filepath.Join(tmp, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))

	badPath := filepath.Join(blocker, "creds.json")

	fake := &fakeAuthClient{
		beginResp: &authclient.DeviceAuth{DeviceCode: "d", UserCode: "U", ExpiresIn: 600},
		pollResp:  &authclient.Tokens{AccessToken: "at", RefreshToken: "rt", ExpiresAt: time.Now().Add(time.Hour)},
	}

	ctrl := &credentialController{
		log:       logrus.New(),
		target:    credentialTarget{issuerURL: "https://i", clientID: "panda", enabled: true},
		newClient: func(credentialTarget) authclient.Client { return fake },
		newStore: func(_ credentialTarget, c authclient.Client) authstore.Store {
			return authstore.New(logrus.New(), authstore.Config{Path: badPath, AuthClient: c})
		},
	}

	_, err := ctrl.BeginLogin(context.Background())
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return ctrl.LoginState().State == serverapi.AuthLoginError
	}, 2*time.Second, 10*time.Millisecond)
}

func TestCredentialControllerLogout(t *testing.T) {
	t.Parallel()

	fake := &fakeAuthClient{}
	ctrl, path := newTestController(t, fake)
	seedTokens(t, path, &authclient.Tokens{AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour)})

	require.True(t, ctrl.Status().Authenticated)
	require.NoError(t, ctrl.Logout())
	require.False(t, ctrl.Status().Authenticated)
}

func TestCredentialControllerLogoutCancelsPendingLogin(t *testing.T) {
	t.Parallel()

	gate := make(chan struct{})
	fake := &fakeAuthClient{
		beginResp: &authclient.DeviceAuth{DeviceCode: "d", UserCode: "U", ExpiresIn: 600},
		pollResp:  &authclient.Tokens{AccessToken: "at", ExpiresAt: time.Now().Add(time.Hour)},
		pollGate:  gate,
	}
	ctrl, _ := newTestController(t, fake)

	_, err := ctrl.BeginLogin(context.Background())
	require.NoError(t, err)
	require.Equal(t, serverapi.AuthLoginPending, ctrl.LoginState().State)

	require.NoError(t, ctrl.Logout())
	require.Equal(t, serverapi.AuthLoginNone, ctrl.LoginState().State)

	close(gate) // let the orphaned poller unwind without affecting state
}

// --- handler-level tests, including the nil-controller (no interactive auth) path ---

func TestAuthHandlersNilController(t *testing.T) {
	t.Parallel()

	s := &service{} // credentials == nil

	var status serverapi.AuthStatusResponse
	rec := callJSON(t, s.handleAuthStatus, http.MethodGet)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &status))
	require.False(t, status.Enabled)

	var login serverapi.AuthLoginResponse
	rec = callJSON(t, s.handleAuthLogin, http.MethodPost)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &login))
	require.False(t, login.Enabled)

	var state serverapi.AuthLoginStateResponse
	rec = callJSON(t, s.handleAuthLoginState, http.MethodGet)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &state))
	require.Equal(t, serverapi.AuthLoginNone, state.State)

	rec = callJSON(t, s.handleAuthLogout, http.MethodPost)
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestAuthStatusHandlerReportsController(t *testing.T) {
	t.Parallel()

	ctrl, path := newTestController(t, &fakeAuthClient{})
	seedTokens(t, path, &authclient.Tokens{AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour)})

	s := &service{credentials: ctrl}

	rec := callJSON(t, s.handleAuthStatus, http.MethodGet)
	require.Equal(t, http.StatusOK, rec.Code)

	var status serverapi.AuthStatusResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &status))
	require.True(t, status.Enabled)
	require.True(t, status.Authenticated)
	require.True(t, status.RefreshTokenPresent)

	// Crucially, no token field is present in the JSON wire form.
	require.NotContains(t, rec.Body.String(), `"access_token"`)
	require.NotContains(t, rec.Body.String(), `"refresh_token"`)
}

func callJSON(t *testing.T, h http.HandlerFunc, method string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, "/api/v1/auth", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	return rec
}

// TestBeginLoginScopeSelection verifies login requests the proxy-advertised
// scopes (never the configured ones) and aborts before minting a token when
// scope discovery fails, while leaving the credential-file keying
// (issuer/client/resource) untouched.
func TestBeginLoginScopeSelection(t *testing.T) {
	t.Parallel()

	t.Run("proxy scopes replace configured scopes", func(t *testing.T) {
		t.Parallel()

		var gotScopes []string

		path := filepath.Join(t.TempDir(), "creds.json")
		fake := &fakeAuthClient{
			beginResp: &authclient.DeviceAuth{DeviceCode: "d", UserCode: "U", ExpiresIn: 600},
			pollResp:  &authclient.Tokens{AccessToken: "at", ExpiresAt: time.Now().Add(time.Hour)},
		}

		ctrl := &credentialController{
			log: logrus.New(),
			target: credentialTarget{
				issuerURL: "https://issuer.example", clientID: "panda", resource: "https://r",
				enabled: true, scopes: []string{"stale-config-scope"},
			},
			discoverLoginAuth: func(context.Context) (proxy.AuthMetadataResponse, error) {
				return proxy.AuthMetadataResponse{
					IssuerURL: "https://issuer.example",
					Scopes:    []string{"openid", "workflows"},
				}, nil
			},
			newClient: func(tg credentialTarget) authclient.Client {
				gotScopes = tg.scopes

				return fake
			},
			newStore: func(_ credentialTarget, c authclient.Client) authstore.Store {
				return authstore.New(logrus.New(), authstore.Config{Path: path, AuthClient: c})
			},
		}

		_, err := ctrl.BeginLogin(context.Background())
		require.NoError(t, err)
		require.Equal(t, []string{"openid", "workflows"}, gotScopes, "configured scopes must not be used")

		require.Eventually(t, func() bool {
			return ctrl.LoginState().State == serverapi.AuthLoginAuthenticated
		}, 2*time.Second, 10*time.Millisecond)
	})

	t.Run("discovery failure aborts login before minting a token", func(t *testing.T) {
		t.Parallel()

		clientBuilt := false
		ctrl := &credentialController{
			log: logrus.New(),
			target: credentialTarget{
				issuerURL: "https://issuer.example", clientID: "panda",
				enabled: true, scopes: []string{"stale-config-scope"},
			},
			discoverLoginAuth: func(context.Context) (proxy.AuthMetadataResponse, error) {
				return proxy.AuthMetadataResponse{}, errors.New("proxy unreachable")
			},
			newClient: func(credentialTarget) authclient.Client {
				clientBuilt = true

				return &fakeAuthClient{}
			},
			newStore: func(_ credentialTarget, c authclient.Client) authstore.Store {
				return authstore.New(logrus.New(), authstore.Config{Path: filepath.Join(t.TempDir(), "c.json"), AuthClient: c})
			},
		}

		_, err := ctrl.BeginLogin(context.Background())
		require.Error(t, err)
		require.Contains(t, err.Error(), "proxy unreachable")
		require.False(t, clientBuilt, "login must not mint a token when scopes are unknown")
		require.Equal(t, serverapi.AuthLoginNone, ctrl.LoginState().State)
	})
}

// TestBeginLoginIssuerGate verifies login refuses to start a device flow when
// the proxy advertises a different issuer than the configured one — the
// advertised scope set is only meaningful there, and the flow would die in the
// browser with the issuer's invalid_scope error. A trailing-slash difference or
// an older proxy that advertises no issuer must not trip the gate.
func TestBeginLoginIssuerGate(t *testing.T) {
	t.Parallel()

	newCtrl := func(t *testing.T, adv proxy.AuthMetadataResponse, clientBuilt *bool) *credentialController {
		t.Helper()

		return &credentialController{
			log: logrus.New(),
			target: credentialTarget{
				issuerURL: "https://dex.example", clientID: "panda", enabled: true,
			},
			discoverLoginAuth: func(context.Context) (proxy.AuthMetadataResponse, error) {
				return adv, nil
			},
			newClient: func(credentialTarget) authclient.Client {
				*clientBuilt = true

				return &fakeAuthClient{
					beginResp: &authclient.DeviceAuth{DeviceCode: "d", UserCode: "U", ExpiresIn: 600},
					pollResp:  &authclient.Tokens{AccessToken: "at", ExpiresAt: time.Now().Add(time.Hour)},
				}
			},
			newStore: func(_ credentialTarget, c authclient.Client) authstore.Store {
				return authstore.New(logrus.New(), authstore.Config{
					Path: filepath.Join(t.TempDir(), "c.json"), AuthClient: c,
				})
			},
		}
	}

	t.Run("advertised issuer mismatch refuses the login", func(t *testing.T) {
		t.Parallel()

		clientBuilt := false
		ctrl := newCtrl(t, proxy.AuthMetadataResponse{
			IssuerURL: "https://authentik.example/application/o/panda-proxy/",
			Scopes:    []string{"openid", "workflows"},
		}, &clientBuilt)

		_, err := ctrl.BeginLogin(context.Background())
		require.Error(t, err)
		require.Contains(t, err.Error(), "https://authentik.example/application/o/panda-proxy/")
		require.Contains(t, err.Error(), "https://dex.example")
		require.Contains(t, err.Error(), "panda init")
		require.False(t, clientBuilt, "login must not mint a device code at a mismatched issuer")
		require.Equal(t, serverapi.AuthLoginNone, ctrl.LoginState().State)
	})

	t.Run("trailing slash is not a different issuer", func(t *testing.T) {
		t.Parallel()

		clientBuilt := false
		ctrl := newCtrl(t, proxy.AuthMetadataResponse{
			IssuerURL: "https://dex.example/",
			Scopes:    []string{"openid"},
		}, &clientBuilt)

		_, err := ctrl.BeginLogin(context.Background())
		require.NoError(t, err)
		require.True(t, clientBuilt)
	})

	t.Run("proxy without advertised issuer skips the gate", func(t *testing.T) {
		t.Parallel()

		clientBuilt := false
		ctrl := newCtrl(t, proxy.AuthMetadataResponse{
			Scopes: []string{"openid"},
		}, &clientBuilt)

		_, err := ctrl.BeginLogin(context.Background())
		require.NoError(t, err)
		require.True(t, clientBuilt)
	})
}

// TestFetchProxyLoginAuth verifies login-auth discovery reads /auth/metadata,
// returns the advertised issuer and scopes (empty scopes are a valid answer),
// and errors on any unreachable or non-200 proxy so the caller can fail the
// login loudly.
func TestFetchProxyLoginAuth(t *testing.T) {
	t.Parallel()

	t.Run("empty url errors", func(t *testing.T) {
		t.Parallel()

		_, err := fetchProxyLoginAuth(context.Background(), "")
		require.Error(t, err)
	})

	t.Run("unreachable proxy errors", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.NotFoundHandler())
		url := srv.URL
		srv.Close()

		_, err := fetchProxyLoginAuth(context.Background(), url)
		require.Error(t, err)
	})

	tests := []struct {
		name       string
		handler    http.HandlerFunc
		wantIssuer string
		wantScopes []string
		wantErr    bool
	}{
		{
			name: "advertised issuer and scopes returned",
			handler: func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/auth/metadata", r.URL.Path)
				_ = json.NewEncoder(w).Encode(proxy.AuthMetadataResponse{
					Enabled:   true,
					IssuerURL: "https://issuer.example/",
					Scopes:    []string{"openid", "email", "groups", "offline_access", "workflows"},
				})
			},
			wantIssuer: "https://issuer.example/",
			wantScopes: []string{"openid", "email", "groups", "offline_access", "workflows"},
		},
		{
			name: "empty scopes are a valid result",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(proxy.AuthMetadataResponse{Enabled: true})
			},
			wantScopes: nil,
		},
		{
			name: "non-200 errors",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(tt.handler)
			defer srv.Close()

			meta, err := fetchProxyLoginAuth(context.Background(), srv.URL)
			if tt.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantIssuer, meta.IssuerURL)
			require.Equal(t, tt.wantScopes, meta.Scopes)
		})
	}
}
