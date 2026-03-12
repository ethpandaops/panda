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
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
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
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type"`
	ExpiresIn    int       `json:"expires_in"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// Config configures the OAuth client.
type Config struct {
	// IssuerURL is the OIDC issuer URL (e.g., https://dex.example.com).
	IssuerURL string

	// ClientID is the OAuth client ID.
	ClientID string

	// Resource is the OAuth protected resource to request tokens for.
	// Defaults to IssuerURL when omitted.
	Resource string

	// RedirectPort is the local port for the callback server.
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
	if cfg.RedirectPort == 0 {
		cfg.RedirectPort = 8085
	}

	if cfg.Resource == "" {
		cfg.Resource = strings.TrimSuffix(cfg.IssuerURL, "/")
	}

	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{"openid", "email", "groups", "offline_access"}
	}

	return &client{
		log:  log.WithField("component", "oauth-client"),
		cfg:  cfg,
		http: &http.Client{Timeout: 30 * time.Second},
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

	// Start callback server.
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	server, err := c.startCallbackServer(ctx, state, codeCh, errCh)
	if err != nil {
		return nil, fmt.Errorf("starting callback server: %w", err)
	}

	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	// Build authorization URL.
	authURL := c.buildAuthURL(state, challenge)

	// Open browser.
	c.log.WithField("url", authURL).Info("Opening browser for authentication")
	fmt.Printf("\nPlease open the following URL in your browser to authenticate:\n\n%s\n\n", authURL)
	fmt.Println("Waiting for authentication...")

	// Wait for callback or context cancellation.
	var code string

	select {
	case code = <-codeCh:
		c.log.Debug("Received authorization code")
	case err := <-errCh:
		return nil, fmt.Errorf("callback error: %w", err)
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// Exchange code for tokens.
	tokens, err := c.exchangeCode(ctx, code, verifier)
	if err != nil {
		return nil, fmt.Errorf("exchanging code: %w", err)
	}

	return tokens, nil
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
		"resource":      {c.cfg.Resource},
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
	if resolvedRefreshToken == "" {
		resolvedRefreshToken = refreshToken
	}

	return &Tokens{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: resolvedRefreshToken,
		TokenType:    tokenResp.TokenType,
		ExpiresIn:    tokenResp.ExpiresIn,
		ExpiresAt:    time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second),
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
func (c *client) buildAuthURL(state, challenge string) string {
	redirectURI := fmt.Sprintf("http://localhost:%d/callback", c.cfg.RedirectPort)

	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {c.cfg.ClientID},
		"redirect_uri":          {redirectURI},
		"scope":                 {strings.Join(c.cfg.Scopes, " ")},
		"resource":              {c.cfg.Resource},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}

	return c.oidc.AuthorizationEndpoint + "?" + params.Encode()
}

// startCallbackServer starts the local callback server.
func (c *client) startCallbackServer(_ context.Context, expectedState string, codeCh chan<- string, errCh chan<- error) (*http.Server, error) {
	mux := http.NewServeMux()

	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		state := r.URL.Query().Get("state")
		if state != expectedState {
			errCh <- fmt.Errorf("state mismatch")
			http.Error(w, "State mismatch", http.StatusBadRequest)

			return
		}

		code := r.URL.Query().Get("code")
		if code == "" {
			err := r.URL.Query().Get("error")
			desc := r.URL.Query().Get("error_description")
			errCh <- fmt.Errorf("oauth error: %s - %s", err, desc)
			http.Error(w, fmt.Sprintf("Error: %s - %s", err, desc), http.StatusBadRequest)

			return
		}

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

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(buildSuccessPage(user)))

		codeCh <- code
	})

	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", c.cfg.RedirectPort),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("callback server error: %w", err)
		}
	}()

	// Give server time to start.
	time.Sleep(100 * time.Millisecond)

	return server, nil
}

// exchangeCode exchanges an authorization code for tokens.
func (c *client) exchangeCode(ctx context.Context, code, verifier string) (*Tokens, error) {
	redirectURI := fmt.Sprintf("http://localhost:%d/callback", c.cfg.RedirectPort)

	data := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {c.cfg.ClientID},
		"code_verifier": {verifier},
		"resource":      {c.cfg.Resource},
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
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		TokenType:    tokenResp.TokenType,
		ExpiresIn:    tokenResp.ExpiresIn,
		ExpiresAt:    time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second),
	}, nil
}

// requestDeviceCode requests a device authorization from the server.
func (c *client) requestDeviceCode(ctx context.Context) (*deviceAuthResponse, error) {
	data := url.Values{
		"client_id": {c.cfg.ClientID},
		"resource":  {c.cfg.Resource},
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
		"resource":    {c.cfg.Resource},
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
			AccessToken:  tokenResp.AccessToken,
			RefreshToken: tokenResp.RefreshToken,
			TokenType:    tokenResp.TokenType,
			ExpiresIn:    tokenResp.ExpiresIn,
			ExpiresAt:    time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second),
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
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
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
