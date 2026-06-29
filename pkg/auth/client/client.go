// Package client provides an OAuth client (device-authorization, refresh, and
// client-credentials grants) for authenticating against an OIDC issuer.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/internal/version"
)

// Client handles OAuth device-authorization, refresh, and client-credentials
// flows against an OIDC issuer.
type Client interface {
	// BeginDeviceLogin starts the RFC 8628 device authorization flow and
	// returns the details a user needs to approve it. The opaque device code in
	// the result is exchanged for tokens by PollDeviceLogin; only the
	// verification URL and user code are meant to be shown to the user.
	BeginDeviceLogin(ctx context.Context) (*DeviceAuth, error)

	// PollDeviceLogin polls the token endpoint until the device authorization
	// is approved (returning tokens), denied, or expired (returning an error).
	PollDeviceLogin(ctx context.Context, device *DeviceAuth) (*Tokens, error)

	// Refresh refreshes an access token using a refresh token.
	Refresh(ctx context.Context, refreshToken string) (*Tokens, error)

	// ClientCredentials mints an access token using the OAuth2
	// client_credentials grant with Authentik's service-account form
	// (client_id + username + password). No refresh token is issued;
	// callers re-mint before expiry.
	ClientCredentials(ctx context.Context) (*Tokens, error)
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

// DefaultRefreshFraction is the elapsed-lifetime fraction at which an access
// token is proactively refreshed: at 0.5 it refreshes once half its lifetime
// has passed (e.g. ~30 min into a 1h token), leaving a wide margin so a request
// never races a just-expired token.
const DefaultRefreshFraction = 0.5

// ShouldRefresh reports whether a token expiring at expiresAt (minted with an
// original lifetime of expiresIn seconds) should be proactively refreshed at
// the moment now.
//
// It returns true once the token is within buffer of expiry, or — when the
// original lifetime is known — once it has passed refreshFraction of that
// lifetime. refreshFraction is the elapsed-life fraction at which to refresh
// (e.g. 0.5 refreshes at the halfway point, well before expiry, so a request
// never races a just-expired token). A zero expiresAt (unknown expiry) only
// triggers on the buffer check.
func ShouldRefresh(now, expiresAt time.Time, expiresIn int, buffer time.Duration, refreshFraction float64) bool {
	if expiresAt.IsZero() {
		return false
	}

	if now.Add(buffer).After(expiresAt) {
		return true
	}

	if expiresIn > 0 && refreshFraction > 0 && refreshFraction < 1 {
		lifetime := time.Duration(expiresIn) * time.Second
		refreshAt := expiresAt.Add(-time.Duration(float64(lifetime) * (1 - refreshFraction)))

		if now.After(refreshAt) {
			return true
		}
	}

	return false
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

	// Username is the service-account username for the client_credentials
	// grant (Authentik service-account form). Unused by interactive flows.
	Username string

	// Password is the service-account app password for the
	// client_credentials grant. Unused by interactive flows.
	Password string

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
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete,omitempty"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// DeviceAuth carries the result of starting a device authorization flow. The
// VerificationURI and UserCode are safe to show to the user; the DeviceCode is
// the secret the poller exchanges for tokens and must not be exposed.
type DeviceAuth struct {
	DeviceCode              string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	ExpiresIn               int
	Interval                int
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

// BeginDeviceLogin starts the device authorization flow and returns the user
// verification details plus the opaque device code used to poll for tokens.
func (c *client) BeginDeviceLogin(ctx context.Context) (*DeviceAuth, error) {
	if err := c.discover(ctx); err != nil {
		return nil, fmt.Errorf("discovering auth config: %w", err)
	}

	if c.oidc.DeviceAuthorizationEndpoint == "" {
		return nil, fmt.Errorf("server does not support device authorization flow")
	}

	resp, err := c.requestDeviceCode(ctx)
	if err != nil {
		return nil, fmt.Errorf("requesting device code: %w", err)
	}

	return &DeviceAuth{
		DeviceCode:              resp.DeviceCode,
		UserCode:                resp.UserCode,
		VerificationURI:         resp.VerificationURI,
		VerificationURIComplete: resp.VerificationURIComplete,
		ExpiresIn:               resp.ExpiresIn,
		Interval:                resp.Interval,
	}, nil
}

// PollDeviceLogin polls the token endpoint until the device authorization in
// device is approved, denied, or expired.
func (c *client) PollDeviceLogin(ctx context.Context, device *DeviceAuth) (*Tokens, error) {
	if device == nil || device.DeviceCode == "" {
		return nil, fmt.Errorf("no device code to poll")
	}

	if err := c.discover(ctx); err != nil {
		return nil, fmt.Errorf("discovering auth config: %w", err)
	}

	interval := max(time.Duration(device.Interval)*time.Second, 5*time.Second)

	return c.pollDeviceToken(ctx, device.DeviceCode, interval)
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
		// Provider did not rotate the refresh token; keep the old one. A zero
		// issued-at signals the refresh token was reused rather than reissued.
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

// ClientCredentials mints an access token using the client_credentials grant.
// It POSTs Authentik's service-account form (grant_type=client_credentials +
// client_id + username + password) to the issuer's token endpoint. The
// returned Tokens carry no refresh token; callers re-mint before expiry.
func (c *client) ClientCredentials(ctx context.Context) (*Tokens, error) {
	if c.cfg.Username == "" || c.cfg.Password == "" {
		return nil, fmt.Errorf("client_credentials grant requires username and password")
	}

	if err := c.discover(ctx); err != nil {
		return nil, fmt.Errorf("discovering OIDC config: %w", err)
	}

	data := url.Values{
		"grant_type": {"client_credentials"},
		"client_id":  {c.cfg.ClientID},
		"username":   {c.cfg.Username},
		"password":   {c.cfg.Password},
	}

	// offline_access is meaningless for client_credentials (no refresh token).
	if scopes := scopesWithoutOfflineAccess(c.cfg.Scopes); len(scopes) > 0 {
		data.Set("scope", strings.Join(scopes, " "))
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

	if tokenResp.AccessToken == "" {
		return nil, fmt.Errorf("token endpoint returned no access token")
	}

	return &Tokens{
		AccessToken: tokenResp.AccessToken,
		TokenType:   tokenResp.TokenType,
		ExpiresIn:   tokenResp.ExpiresIn,
		ExpiresAt:   time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second),
	}, nil
}

// scopesWithoutOfflineAccess filters offline_access from a scope list.
func scopesWithoutOfflineAccess(scopes []string) []string {
	filtered := make([]string, 0, len(scopes))

	for _, scope := range scopes {
		if scope != "offline_access" {
			filtered = append(filtered, scope)
		}
	}

	return filtered
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

// requestDeviceCode requests a device authorization from the server.
func (c *client) requestDeviceCode(ctx context.Context) (*deviceAuthResponse, error) {
	data := url.Values{
		"client_id": {c.cfg.ClientID},
		"scope":     {strings.Join(c.cfg.Scopes, " ")},
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
