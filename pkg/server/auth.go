package server

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	authclient "github.com/ethpandaops/panda/pkg/auth/client"
	authstore "github.com/ethpandaops/panda/pkg/auth/store"
	"github.com/ethpandaops/panda/pkg/serverapi"
)

// minLoginPollWindow bounds how long the server keeps polling for device
// approval when the provider does not advertise an expiry.
const minLoginPollWindow = 5 * time.Minute

// credentialTarget is the resolved proxy auth configuration the controller
// operates against. It mirrors the values the proxy client uses to build its
// own credential store so both resolve to the same on-disk credential file.
type credentialTarget struct {
	issuerURL string
	clientID  string
	resource  string
	enabled   bool
}

// credentialController owns interactive proxy credentials on behalf of the
// server. The CLI drives login/logout/status through it over the HTTP API; the
// access and refresh tokens are minted into and held by the server and are
// never returned to the caller. This keeps the server the single owner and
// single refresher of the credential, so no other process competes over the
// rotating refresh token.
type credentialController struct {
	log    logrus.FieldLogger
	target credentialTarget

	// newClient and newStore are injected so tests can supply fakes; in
	// production they build the real OAuth client and on-disk store.
	newClient func(credentialTarget) authclient.Client
	newStore  func(credentialTarget, authclient.Client) authstore.Store

	mu      sync.Mutex
	pending *pendingLogin
}

// pendingLogin tracks an in-flight device-authorization login.
type pendingLogin struct {
	state  serverapi.AuthLoginState
	err    string
	cancel context.CancelFunc
}

// newCredentialController builds a controller for the resolved proxy auth
// metadata. It returns nil when no interactive auth is configured (e.g. the
// client_credentials service-account grant, which has no human login flow).
func newCredentialController(log logrus.FieldLogger, meta *serverapi.ProxyAuthMetadataResponse) *credentialController {
	if meta == nil || !meta.Enabled {
		return nil
	}

	target := credentialTarget{
		issuerURL: meta.IssuerURL,
		clientID:  meta.ClientID,
		resource:  meta.Resource,
		enabled:   meta.Enabled,
	}

	return &credentialController{
		log:    log.WithField("component", "credential-controller"),
		target: target,
		newClient: func(t credentialTarget) authclient.Client {
			return authclient.New(log, authclient.Config{
				IssuerURL: t.issuerURL,
				ClientID:  t.clientID,
				Resource:  t.resource,
			})
		},
		newStore: func(t credentialTarget, c authclient.Client) authstore.Store {
			return authstore.New(log, authstore.Config{
				AuthClient: c,
				IssuerURL:  t.issuerURL,
				ClientID:   t.clientID,
				Resource:   t.resource,
			})
		},
	}
}

// Status reports the current credential state without exposing any token.
func (c *credentialController) Status() serverapi.AuthStatusResponse {
	resp := serverapi.AuthStatusResponse{
		Enabled:   c.target.enabled,
		IssuerURL: c.target.issuerURL,
		ClientID:  c.target.clientID,
		Resource:  c.target.resource,
	}

	store := c.newStore(c.target, nil)
	resp.CredentialsPath = store.Path()

	tokens, err := store.Load()
	if err != nil || tokens == nil {
		return resp
	}

	hasValidAccess := tokens.ExpiresAt.After(time.Now())
	resp.Authenticated = hasValidAccess || tokens.RefreshToken != ""
	resp.Expired = !hasValidAccess
	resp.RefreshTokenPresent = tokens.RefreshToken != ""

	expiresAt := tokens.ExpiresAt
	resp.ExpiresAt = &expiresAt

	if !tokens.RefreshTokenIssuedAt.IsZero() {
		issuedAt := tokens.RefreshTokenIssuedAt
		resp.RefreshTokenIssuedAt = &issuedAt
	}

	return resp
}

// BeginLogin starts a device-authorization login and spawns a background poller
// that persists the tokens on approval. It returns the user-facing verification
// details; the device code stays server-side.
func (c *credentialController) BeginLogin(ctx context.Context) (serverapi.AuthLoginResponse, error) {
	client := c.newClient(c.target)

	device, err := client.BeginDeviceLogin(ctx)
	if err != nil {
		return serverapi.AuthLoginResponse{}, fmt.Errorf("starting device login: %w", err)
	}

	window := minLoginPollWindow
	if device.ExpiresIn > 0 {
		window = time.Duration(device.ExpiresIn) * time.Second
	}

	pollCtx, cancel := context.WithTimeout(context.Background(), window)

	p := &pendingLogin{state: serverapi.AuthLoginPending, cancel: cancel}

	c.mu.Lock()
	if c.pending != nil && c.pending.cancel != nil {
		c.pending.cancel()
	}
	c.pending = p
	c.mu.Unlock()

	go c.runLogin(pollCtx, client, device, p)

	return serverapi.AuthLoginResponse{
		Enabled:                 true,
		VerificationURI:         device.VerificationURI,
		VerificationURIComplete: device.VerificationURIComplete,
		UserCode:                device.UserCode,
		ExpiresIn:               device.ExpiresIn,
		Interval:                device.Interval,
	}, nil
}

// LoginState reports progress of the most recent device login.
func (c *credentialController) LoginState() serverapi.AuthLoginStateResponse {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.pending == nil {
		return serverapi.AuthLoginStateResponse{State: serverapi.AuthLoginNone}
	}

	return serverapi.AuthLoginStateResponse{State: c.pending.state, Error: c.pending.err}
}

// Logout clears the stored credential and cancels any in-flight login.
func (c *credentialController) Logout() error {
	c.mu.Lock()
	if c.pending != nil && c.pending.cancel != nil {
		c.pending.cancel()
	}
	c.pending = nil
	c.mu.Unlock()

	return c.newStore(c.target, nil).Clear()
}

// Stop cancels any in-flight login so its poller goroutine exits. The stored
// credential is left intact.
func (c *credentialController) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.pending != nil && c.pending.cancel != nil {
		c.pending.cancel()
	}

	c.pending = nil
}

// runLogin polls for device approval and persists the resulting tokens. The
// terminal state and the credential write are committed under the lock only if
// this attempt is still the current pending login, so a logout or a newer login
// that supersedes it can neither be clobbered nor re-authenticated by a
// late-returning poll.
func (c *credentialController) runLogin(
	ctx context.Context,
	client authclient.Client,
	device *authclient.DeviceAuth,
	p *pendingLogin,
) {
	defer p.cancel()

	tokens, err := client.PollDeviceLogin(ctx, device)

	c.mu.Lock()
	defer c.mu.Unlock()

	// Superseded by a newer login or cancelled by logout; drop the result.
	if c.pending != p {
		return
	}

	if err != nil {
		p.state = serverapi.AuthLoginError
		p.err = err.Error()

		return
	}

	if saveErr := c.newStore(c.target, client).Save(tokens); saveErr != nil {
		p.state = serverapi.AuthLoginError
		p.err = saveErr.Error()

		return
	}

	p.state = serverapi.AuthLoginAuthenticated
}

// handleAuthStatus reports the server's credential state (GET /api/v1/auth/status).
func (s *service) handleAuthStatus(w http.ResponseWriter, _ *http.Request) {
	if s.credentials == nil {
		writeJSON(w, http.StatusOK, serverapi.AuthStatusResponse{})

		return
	}

	writeJSON(w, http.StatusOK, s.credentials.Status())
}

// handleAuthLogin starts a device-authorization login (POST /api/v1/auth/login).
func (s *service) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if s.credentials == nil {
		writeJSON(w, http.StatusOK, serverapi.AuthLoginResponse{Enabled: false})

		return
	}

	resp, err := s.credentials.BeginLogin(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, err.Error())

		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleAuthLoginState reports in-flight login progress (GET /api/v1/auth/login).
func (s *service) handleAuthLoginState(w http.ResponseWriter, _ *http.Request) {
	if s.credentials == nil {
		writeJSON(w, http.StatusOK, serverapi.AuthLoginStateResponse{State: serverapi.AuthLoginNone})

		return
	}

	writeJSON(w, http.StatusOK, s.credentials.LoginState())
}

// handleAuthLogout clears stored credentials (POST /api/v1/auth/logout).
func (s *service) handleAuthLogout(w http.ResponseWriter, _ *http.Request) {
	if s.credentials == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})

		return
	}

	if err := s.credentials.Logout(); err != nil {
		writeAPIError(w, http.StatusInternalServerError, fmt.Sprintf("clearing credentials: %v", err))

		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
