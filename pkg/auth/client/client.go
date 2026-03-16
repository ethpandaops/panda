// Package client provides an OAuth PKCE client for local authentication.
package client

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/internal/version"
	"github.com/ethpandaops/panda/pkg/auth"
)

// Client handles OAuth PKCE authentication flow.
type Client interface {
	// Login performs the OAuth PKCE flow and returns tokens.
	Login(ctx context.Context) (*Tokens, error)

	// Refresh refreshes an access token using a refresh token.
	Refresh(ctx context.Context, refreshToken string) (*Tokens, error)
}

// Tokens contains the authentication tokens.
type Tokens struct {
	AccessToken          string    `json:"access_token"`
	RefreshToken         string    `json:"refresh_token,omitempty"`
	TokenType            string    `json:"token_type"`
	ExpiresIn            int       `json:"expires_in"`
	ExpiresAt            time.Time `json:"expires_at"`
	RefreshTokenIssuedAt time.Time `json:"refresh_token_issued_at,omitempty"`
}

// Config configures the OAuth client.
type Config struct {
	// IssuerURL is the OIDC issuer URL (e.g., https://dex.example.com).
	IssuerURL string

	// ClientID is the OAuth client ID.
	ClientID string

	// Resource is the optional OAuth protected resource to request tokens for.
	// Leave empty for standard OIDC providers that do not use RFC 8707 resource parameters.
	Resource string

	// BrandingURL is the URL to fetch branding config from (optional).
	// When set, the client fetches SuccessPageConfig from this endpoint
	// before login so it can resolve branding rules client-side in OIDC mode.
	BrandingURL string

	// RedirectPort is the local port for the callback server.
	// When zero, a free loopback port is selected automatically.
	RedirectPort int

	// Scopes are the OAuth scopes to request.
	Scopes []string

	// Headless uses the device authorization flow (RFC 8628) instead of
	// the local callback server. Use for SSH or headless environments.
	Headless bool
}

// client implements the Client interface.
type client struct {
	log    logrus.FieldLogger
	cfg    Config
	http   *http.Client
	oidc   *OIDCConfig
	loaded bool
}

// OIDCConfig contains OIDC discovery configuration.
type OIDCConfig struct {
	Issuer                      string   `json:"issuer"`
	AuthorizationEndpoint       string   `json:"authorization_endpoint"`
	TokenEndpoint               string   `json:"token_endpoint"`
	DeviceAuthorizationEndpoint string   `json:"device_authorization_endpoint"`
	JwksURI                     string   `json:"jwks_uri"`
	ScopesSupported             []string `json:"scopes_supported"`
}

// deviceAuthResponse is the RFC 8628 device authorization response.
type deviceAuthResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// New creates a new OAuth client.
func New(log logrus.FieldLogger, cfg Config) Client {
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{"openid", "email", "groups", "offline_access"}
	}

	return &client{
		log:  log.WithField("component", "oauth-client"),
		cfg:  cfg,
		http: &http.Client{Transport: &version.Transport{}, Timeout: 30 * time.Second},
	}
}

// Login performs the OAuth PKCE flow.
func (c *client) Login(ctx context.Context) (*Tokens, error) {
	if c.cfg.Headless {
		return c.loginDevice(ctx)
	}

	// Discover OIDC configuration.
	if err := c.discover(ctx); err != nil {
		return nil, fmt.Errorf("discovering OIDC config: %w", err)
	}

	// Generate PKCE challenge.
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return nil, fmt.Errorf("generating PKCE: %w", err)
	}

	// Generate state.
	state, err := generateState()
	if err != nil {
		return nil, fmt.Errorf("generating state: %w", err)
	}

	// Fetch branding config (best-effort, nil on failure).
	branding := c.fetchBranding(ctx)

	// Start callback server — it exchanges the code for tokens internally.
	tokensCh := make(chan *Tokens, 1)
	errCh := make(chan error, 1)

	server, redirectURI, err := c.startCallbackServer(state, verifier, branding, tokensCh, errCh)
	if err != nil {
		return nil, fmt.Errorf("starting callback server: %w", err)
	}

	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	// Build authorization URL.
	authURL := c.buildAuthURL(state, challenge, redirectURI)

	// Open browser.
	c.log.WithField("url", authURL).Info("Opening browser for authentication")
	fmt.Printf("\nPlease open the following URL in your browser to authenticate:\n\n%s\n\n", authURL)
	fmt.Println("Waiting for authentication...")

	// Wait for tokens or context cancellation.
	select {
	case tokens := <-tokensCh:
		c.log.Debug("Received tokens from callback")
		return tokens, nil
	case err := <-errCh:
		return nil, fmt.Errorf("callback error: %w", err)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// loginDevice performs the RFC 8628 device authorization flow.
// It requests a device code, displays the user code, and polls until authorized.
func (c *client) loginDevice(ctx context.Context) (*Tokens, error) {
	if err := c.discover(ctx); err != nil {
		return nil, fmt.Errorf("discovering auth config: %w", err)
	}

	if c.oidc.DeviceAuthorizationEndpoint == "" {
		return nil, fmt.Errorf("server does not support device authorization flow")
	}

	// Request device code.
	deviceResp, err := c.requestDeviceCode(ctx)
	if err != nil {
		return nil, fmt.Errorf("requesting device code: %w", err)
	}

	// Display instructions.
	fmt.Printf("\nOpen %s in your browser\nand enter the code:\n\n  %s\n\n",
		deviceResp.VerificationURI, deviceResp.UserCode)
	fmt.Println("Waiting for authorization... (press Ctrl+C to cancel)")

	// Poll for token.
	interval := max(time.Duration(deviceResp.Interval)*time.Second, 5*time.Second)

	tokens, err := c.pollDeviceToken(ctx, deviceResp.DeviceCode, interval)
	if err != nil {
		return nil, err
	}

	return tokens, nil
}

// Refresh refreshes an access token using a refresh token.
func (c *client) Refresh(ctx context.Context, refreshToken string) (*Tokens, error) {
	// Discover OIDC configuration if not loaded.
	if err := c.discover(ctx); err != nil {
		return nil, fmt.Errorf("discovering OIDC config: %w", err)
	}

	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {c.cfg.ClientID},
	}
	if c.cfg.Resource != "" {
		data.Set("resource", c.cfg.Resource)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.oidc.TokenEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("making request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("token endpoint returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var tokenResp tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	resolvedRefreshToken := tokenResp.RefreshToken
	refreshTokenIssuedAt := time.Now()

	if resolvedRefreshToken == "" {
		// Provider did not rotate the refresh token; keep the old one.
		// The caller will preserve the original RefreshTokenIssuedAt.
		resolvedRefreshToken = refreshToken
		refreshTokenIssuedAt = time.Time{}
	}

	return &Tokens{
		AccessToken:          bearerTokenFromResponse(tokenResp),
		RefreshToken:         resolvedRefreshToken,
		TokenType:            tokenResp.TokenType,
		ExpiresIn:            tokenResp.ExpiresIn,
		ExpiresAt:            time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second),
		RefreshTokenIssuedAt: refreshTokenIssuedAt,
	}, nil
}

// discover fetches OIDC configuration from the issuer.
func (c *client) discover(ctx context.Context) error {
	if c.loaded {
		return nil
	}

	issuer := strings.TrimSuffix(c.cfg.IssuerURL, "/")
	discoveryPaths := []string{
		"/.well-known/openid-configuration",
		"/.well-known/oauth-authorization-server",
	}

	var errs []string

	for _, discoveryPath := range discoveryPaths {
		oidc, err := c.fetchDiscovery(ctx, issuer+discoveryPath)
		if err == nil {
			c.oidc = oidc
			c.loaded = true
			return nil
		}

		errs = append(errs, fmt.Sprintf("%s: %v", discoveryPath, err))
	}

	return fmt.Errorf("discovering auth metadata failed: %s", strings.Join(errs, "; "))
}

func (c *client) fetchDiscovery(ctx context.Context, discoveryURL string) (*OIDCConfig, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("making request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		if len(body) == 0 {
			return nil, fmt.Errorf("discovery endpoint returned status %d", resp.StatusCode)
		}

		return nil, fmt.Errorf("discovery endpoint returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var oidc OIDCConfig
	if err := json.NewDecoder(resp.Body).Decode(&oidc); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	return &oidc, nil
}

// buildAuthURL builds the authorization URL.
func (c *client) buildAuthURL(state, challenge, redirectURI string) string {
	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {c.cfg.ClientID},
		"redirect_uri":          {redirectURI},
		"scope":                 {strings.Join(c.cfg.Scopes, " ")},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	if c.cfg.Resource != "" {
		params.Set("resource", c.cfg.Resource)
	}

	return c.oidc.AuthorizationEndpoint + "?" + params.Encode()
}

// startCallbackServer starts the local callback server.
// The callback handler exchanges the authorization code for tokens, resolves
// branding (from query params in oauth mode, or from ID token claims + branding
// config in OIDC mode), renders the success page, and sends tokens on tokensCh.
func (c *client) startCallbackServer(expectedState, verifier string, branding *auth.SuccessPageConfig, tokensCh chan<- *Tokens, errCh chan<- error) (*http.Server, string, error) {
	mux := http.NewServeMux()

	// We need the redirectURI before registering the handler, but we also
	// need the listener to know the port. Bind the listener first, then
	// capture redirectURI in the closure.
	listenAddr := "localhost:0"
	if c.cfg.RedirectPort > 0 {
		listenAddr = fmt.Sprintf("localhost:%d", c.cfg.RedirectPort)
	}

	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, "", fmt.Errorf("listening on callback port: %w", err)
	}

	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return nil, "", fmt.Errorf("unexpected callback listener address type %T", listener.Addr())
	}

	redirectURI := fmt.Sprintf("http://localhost:%d/callback", tcpAddr.Port)

	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		state := r.URL.Query().Get("state")
		if state != expectedState {
			errCh <- fmt.Errorf("state mismatch")
			http.Error(w, "State mismatch", http.StatusBadRequest)

			return
		}

		code := r.URL.Query().Get("code")
		if code == "" {
			oauthErr := r.URL.Query().Get("error")
			desc := r.URL.Query().Get("error_description")
			errCh <- fmt.Errorf("oauth error: %s - %s", oauthErr, desc)
			http.Error(w, fmt.Sprintf("Error: %s - %s", oauthErr, desc), http.StatusBadRequest)

			return
		}

		// Exchange code for tokens. Use a detached context so the exchange
		// completes even if the browser closes the connection early.
		exchangeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		tokens, err := c.exchangeCode(exchangeCtx, code, verifier, redirectURI)
		if err != nil {
			errCh <- fmt.Errorf("exchanging code: %w", err)
			http.Error(w, "Token exchange failed", http.StatusInternalServerError)

			return
		}

		// Build user info for the success page.
		user := c.resolveCallbackUser(r, tokens, branding)

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(buildSuccessPage(user)))

		tokensCh <- tokens
	})

	srv := &http.Server{
		Addr:              listener.Addr().String(),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("callback server error: %w", err)
		}
	}()

	return srv, redirectURI, nil
}

// exchangeCode exchanges an authorization code for tokens.
func (c *client) exchangeCode(ctx context.Context, code, verifier, redirectURI string) (*Tokens, error) {
	data := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {c.cfg.ClientID},
		"code_verifier": {verifier},
	}
	if c.cfg.Resource != "" {
		data.Set("resource", c.cfg.Resource)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.oidc.TokenEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("making request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("token endpoint returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var tokenResp tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	return &Tokens{
		AccessToken:          bearerTokenFromResponse(tokenResp),
		RefreshToken:         tokenResp.RefreshToken,
		TokenType:            tokenResp.TokenType,
		ExpiresIn:            tokenResp.ExpiresIn,
		ExpiresAt:            time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second),
		RefreshTokenIssuedAt: time.Now(),
	}, nil
}

// requestDeviceCode requests a device authorization from the server.
func (c *client) requestDeviceCode(ctx context.Context) (*deviceAuthResponse, error) {
	data := url.Values{
		"client_id": {c.cfg.ClientID},
	}
	if c.cfg.Resource != "" {
		data.Set("resource", c.cfg.Resource)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.oidc.DeviceAuthorizationEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("making request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))

		return nil, fmt.Errorf("device code endpoint returned status %d: %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var deviceResp deviceAuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&deviceResp); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	return &deviceResp, nil
}

// pollDeviceToken polls the token endpoint until the device is authorized.
func (c *client) pollDeviceToken(ctx context.Context, deviceCode string, interval time.Duration) (*Tokens, error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			tokens, pending, err := c.exchangeDeviceCode(ctx, deviceCode)
			if err != nil {
				return nil, err
			}

			if pending {
				continue
			}

			return tokens, nil
		}
	}
}

// exchangeDeviceCode attempts to exchange a device code for tokens.
// Returns pending=true if the user hasn't authorized yet.
func (c *client) exchangeDeviceCode(ctx context.Context, deviceCode string) (tokens *Tokens, pending bool, err error) {
	data := url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {deviceCode},
		"client_id":   {c.cfg.ClientID},
	}
	if c.cfg.Resource != "" {
		data.Set("resource", c.cfg.Resource)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.oidc.TokenEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, false, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("making request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return nil, false, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode == http.StatusOK {
		var tokenResp tokenResponse
		if err := json.Unmarshal(body, &tokenResp); err != nil {
			return nil, false, fmt.Errorf("decoding token response: %w", err)
		}

		return &Tokens{
			AccessToken:          bearerTokenFromResponse(tokenResp),
			RefreshToken:         tokenResp.RefreshToken,
			TokenType:            tokenResp.TokenType,
			ExpiresIn:            tokenResp.ExpiresIn,
			ExpiresAt:            time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second),
			RefreshTokenIssuedAt: time.Now(),
		}, false, nil
	}

	// Parse error response.
	var errResp struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}

	if err := json.Unmarshal(body, &errResp); err != nil {
		return nil, false, fmt.Errorf("token endpoint returned status %d: %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}

	switch errResp.Error {
	case "authorization_pending", "slow_down":
		return nil, true, nil
	case "expired_token":
		return nil, false, fmt.Errorf("device code expired, please restart authentication")
	case "access_denied":
		return nil, false, fmt.Errorf("authorization was denied")
	default:
		return nil, false, fmt.Errorf("token error: %s: %s", errResp.Error, errResp.ErrorDescription)
	}
}

// tokenResponse is the OAuth token endpoint response.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	IDToken      string `json:"id_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

func bearerTokenFromResponse(resp tokenResponse) string {
	if resp.IDToken != "" {
		return resp.IDToken
	}

	return resp.AccessToken
}

// idTokenClaims holds display-relevant claims extracted from an OIDC ID token.
type idTokenClaims struct {
	PreferredUsername string   `json:"preferred_username"`
	Email             string   `json:"email"`
	Groups            []string `json:"groups"`
}

// fetchBranding fetches the SuccessPageConfig from the proxy branding endpoint.
// Returns nil on any error (best-effort).
func (c *client) fetchBranding(ctx context.Context) *auth.SuccessPageConfig {
	if c.cfg.BrandingURL == "" {
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.BrandingURL, nil)
	if err != nil {
		c.log.WithError(err).Debug("Failed to create branding request")
		return nil
	}

	resp, err := c.http.Do(req)
	if err != nil {
		c.log.WithError(err).Debug("Failed to fetch branding config")
		return nil
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var cfg auth.SuccessPageConfig
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		c.log.WithError(err).Debug("Failed to decode branding config")
		return nil
	}

	return &cfg
}

// parseIDTokenClaims extracts display-relevant claims from a JWT ID token
// by base64-decoding the payload. No cryptographic verification is performed
// because the token was just exchanged over TLS and is used only for display.
func parseIDTokenClaims(token string) idTokenClaims {
	parts := strings.SplitN(token, ".", 3)
	if len(parts) < 2 {
		return idTokenClaims{}
	}

	// JWT payload is base64url-encoded without padding.
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return idTokenClaims{}
	}

	var claims idTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return idTokenClaims{}
	}

	return claims
}

// resolveCallbackUser builds a callbackUser from the OAuth callback request.
// In oauth mode (sp_* query params present), uses them directly.
// In OIDC mode, parses ID token claims and resolves branding rules client-side.
func (c *client) resolveCallbackUser(r *http.Request, tokens *Tokens, branding *auth.SuccessPageConfig) callbackUser {
	// If sp_* params are present, the proxy already resolved branding (oauth mode).
	if r.URL.Query().Get("sp_tagline") != "" || r.URL.Query().Get("sp_media_type") != "" {
		user := callbackUser{
			Login:         r.URL.Query().Get("login"),
			AvatarURL:     r.URL.Query().Get("avatar_url"),
			Tagline:       r.URL.Query().Get("sp_tagline"),
			MediaType:     r.URL.Query().Get("sp_media_type"),
			MediaURL:      r.URL.Query().Get("sp_media_url"),
			MediaASCIIB64: r.URL.Query().Get("sp_media_ascii"),
		}

		if orgsParam := r.URL.Query().Get("orgs"); orgsParam != "" {
			user.Orgs = strings.Split(orgsParam, ",")
		}

		return user
	}

	// OIDC mode — extract identity from ID token claims.
	claims := parseIDTokenClaims(tokens.AccessToken)

	login := claims.PreferredUsername
	if login == "" {
		login = claims.Email
	}

	user := callbackUser{
		Login: login,
		Orgs:  claims.Groups,
	}

	// Resolve branding rules if available.
	if branding != nil {
		display := branding.Resolve(user.Login, user.Orgs)
		user.Tagline = display.Tagline

		if display.Media != nil {
			user.MediaType = display.Media.Type
			user.MediaURL = display.Media.URL
			user.MediaASCIIB64 = display.Media.ASCIIArtBase64
		}
	}

	return user
}

// generatePKCE generates a PKCE verifier and challenge.
func generatePKCE() (verifier, challenge string, err error) {
	// Generate 32 random bytes for verifier.
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("generating random bytes: %w", err)
	}

	verifier = base64.RawURLEncoding.EncodeToString(b)

	// Generate challenge: SHA256(verifier) base64url encoded.
	h := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(h[:])

	return verifier, challenge, nil
}

// generateState generates a random state string.
func generateState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating random bytes: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(b), nil
}
