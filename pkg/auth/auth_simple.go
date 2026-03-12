// Package auth provides simplified GitHub-based OAuth for local product edges.
//
// This implements a minimal OAuth 2.1 authorization server that:
// - Delegates identity verification to GitHub
// - Issues signed bearer tokens with proper resource (audience) binding per RFC 8707
// - Validates bearer tokens on protected endpoints
//
// Two client flows are supported:
// 1. PKCE authorization code flow (local browser callback)
// 2. Device authorization grant (RFC 8628, for SSH/headless environments)
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/auth/github"
)

const (
	// Authorization code TTL.
	authCodeTTL = 5 * time.Minute

	// Access token TTL.
	accessTokenTTL = 1 * time.Hour

	// Refresh token TTL.
	refreshTokenTTL = 30 * 24 * time.Hour

	// refreshTokenType distinguishes refresh tokens from access tokens.
	refreshTokenType = "refresh"

	// deviceCodeTTL is how long a user has to complete the device flow.
	deviceCodeTTL = 15 * time.Minute

	// devicePollInterval is the minimum polling interval in seconds per RFC 8628.
	devicePollInterval = 5

	// userCodeAlphabet is uppercase consonants only (no vowels to avoid offensive words).
	userCodeAlphabet = "BCDFGHJKLMNPQRSTVWXZ"

	// userCodeHalfLen is the number of characters per half of the XXXX-XXXX user code.
	userCodeHalfLen = 4
)

// SimpleService is the simplified auth service interface.
type SimpleService interface {
	Start(ctx context.Context) error
	Stop() error
	Enabled() bool
	Middleware() func(http.Handler) http.Handler
	MountRoutes(r chi.Router)
}

// simpleService implements SimpleService.
type simpleService struct {
	log         logrus.FieldLogger
	cfg         Config
	github      *github.Client
	secretKey   []byte
	allowedOrgs []string

	// Pending authorization requests (state -> pendingAuth).
	pending   map[string]*pendingAuth
	pendingMu sync.RWMutex

	// Authorization codes (code -> issuedCode).
	codes   map[string]*issuedCode
	codesMu sync.RWMutex

	// Device authorizations (RFC 8628).
	devices   map[string]*deviceAuth // device_code -> deviceAuth
	userCodes map[string]string      // normalized user_code -> device_code
	devicesMu sync.RWMutex

	// Lifecycle.
	stopCh chan struct{}
}

// pendingAuth stores state during the OAuth flow.
type pendingAuth struct {
	ClientID      string
	RedirectURI   string
	CodeChallenge string
	Resource      string
	State         string
	CreatedAt     time.Time
	DeviceCode    string // non-empty for device flow callbacks
}

// issuedCode is an issued authorization code.
type issuedCode struct {
	Code          string
	ClientID      string
	RedirectURI   string
	Resource      string
	CodeChallenge string
	GitHubLogin   string
	GitHubID      int64
	Orgs          []string
	CreatedAt     time.Time
	Used          bool
}

// deviceAuth stores a pending device authorization request (RFC 8628).
type deviceAuth struct {
	DeviceCode  string
	UserCode    string
	ClientID    string
	Resource    string
	CreatedAt   time.Time
	ExpiresAt   time.Time
	Authorized  bool
	GitHubLogin string
	GitHubID    int64
	Orgs        []string
}

// refreshTokenClaims are the JWT claims for stateless refresh tokens.
type refreshTokenClaims struct {
	jwt.RegisteredClaims
	GitHubLogin string   `json:"github_login"`
	GitHubID    int64    `json:"github_id"`
	Orgs        []string `json:"orgs,omitempty"`
	ClientID    string   `json:"client_id"`
	Resource    string   `json:"resource"`
	TokenType   string   `json:"token_type"`
}

// tokenClaims are the JWT claims for access tokens.
type tokenClaims struct {
	jwt.RegisteredClaims
	GitHubLogin string   `json:"github_login"`
	GitHubID    int64    `json:"github_id"`
	Orgs        []string `json:"orgs,omitempty"`
}

// NewSimpleService creates a new simplified auth service.
// The base URL for OAuth metadata and token issuance is derived from each
// incoming request's Host header, so no static base URL is required.
func NewSimpleService(log logrus.FieldLogger, cfg Config) (SimpleService, error) {
	log = log.WithField("component", "auth")

	if !cfg.Enabled {
		log.Info("Authentication is disabled")
		return &simpleService{log: log, cfg: cfg}, nil
	}

	if cfg.GitHub == nil {
		return nil, fmt.Errorf("github configuration is required when auth is enabled")
	}

	if cfg.Tokens.SecretKey == "" {
		return nil, fmt.Errorf("tokens.secret_key is required when auth is enabled")
	}

	s := &simpleService{
		log:         log,
		cfg:         cfg,
		github:      github.NewClient(log, cfg.GitHub.ClientID, cfg.GitHub.ClientSecret),
		secretKey:   []byte(cfg.Tokens.SecretKey),
		allowedOrgs: cfg.AllowedOrgs,
		pending:     make(map[string]*pendingAuth, 32),
		codes:       make(map[string]*issuedCode, 32),
		devices:     make(map[string]*deviceAuth, 16),
		userCodes:   make(map[string]string, 16),
		stopCh:      make(chan struct{}),
	}

	log.WithFields(logrus.Fields{
		"allowed_orgs": cfg.AllowedOrgs,
	}).Info("Auth service created")

	return s, nil
}

func (s *simpleService) Start(ctx context.Context) error {
	if !s.cfg.Enabled {
		return nil
	}

	// Start cleanup goroutine.
	go s.cleanupLoop()

	s.log.Info("Auth service started")
	return nil
}

func (s *simpleService) Stop() error {
	if !s.cfg.Enabled {
		return nil
	}

	close(s.stopCh)
	s.log.Info("Auth service stopped")
	return nil
}

func (s *simpleService) Enabled() bool {
	return s.cfg.Enabled
}

// MountRoutes mounts auth routes.
func (s *simpleService) MountRoutes(r chi.Router) {
	if !s.cfg.Enabled {
		return
	}

	// Discovery endpoints.
	r.Get("/.well-known/oauth-protected-resource", s.handleResourceMetadata)
	r.Get("/.well-known/oauth-authorization-server", s.handleServerMetadata)

	// OAuth endpoints.
	r.Get("/auth/authorize", s.handleAuthorize)
	r.Get("/auth/callback", s.handleCallback)
	r.Post("/auth/token", s.handleToken)

	// Device authorization endpoints (RFC 8628).
	r.Post("/auth/device/code", s.handleDeviceCode)
	r.Get("/auth/device", s.handleDevicePage)
	r.Post("/auth/device/verify", s.handleDeviceVerify)
}

// Middleware returns bearer-token validation middleware.
func (s *simpleService) Middleware() func(http.Handler) http.Handler {
	if !s.cfg.Enabled {
		return func(next http.Handler) http.Handler { return next }
	}

	publicPaths := map[string]bool{
		"/":                                     true,
		"/health":                               true,
		"/ready":                                true,
		"/.well-known/oauth-protected-resource": true,
		"/.well-known/oauth-authorization-server": true,
	}

	publicPrefixes := []string{"/auth/", "/.well-known/"}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip public paths.
			if publicPaths[r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}
			for _, prefix := range publicPrefixes {
				if strings.HasPrefix(r.URL.Path, prefix) {
					next.ServeHTTP(w, r)
					return
				}
			}

			// Get token from Authorization header.
			authHeader := r.Header.Get("Authorization")
			if !strings.HasPrefix(authHeader, "Bearer ") {
				baseURL := baseURLFromRequest(r)
				s.writeUnauthorized(w, baseURL, "missing or invalid Authorization header")
				return
			}

			tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
			baseURL := baseURLFromRequest(r)

			// Validate token.
			claims := &tokenClaims{}
			token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
				if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
					return nil, fmt.Errorf("unexpected signing method")
				}
				return s.secretKey, nil
			}, jwt.WithIssuer(baseURL), jwt.WithExpirationRequired())

			if err != nil || !token.Valid {
				s.writeUnauthorized(w, baseURL, "invalid token")
				return
			}

			// Validate audience (RFC 8707).
			audienceValid := false
			for _, aud := range claims.Audience {
				if aud == baseURL {
					audienceValid = true
					break
				}
			}
			if !audienceValid {
				s.writeUnauthorized(w, baseURL, "token audience mismatch")
				return
			}

			// Attach user info to context.
			ctx := context.WithValue(r.Context(), authUserKey, &AuthUser{
				GitHubLogin: claims.GitHubLogin,
				GitHubID:    claims.GitHubID,
				Orgs:        claims.Orgs,
			})

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// AuthUser is the authenticated user info attached to request context.
type AuthUser struct {
	GitHubLogin string
	GitHubID    int64
	Orgs        []string
}

// GetAuthUser returns the authenticated user from context.
func GetAuthUser(ctx context.Context) *AuthUser {
	user, _ := ctx.Value(authUserKey).(*AuthUser)
	return user
}

type authUserKeyType string

const authUserKey authUserKeyType = "auth_user"

// cleanupLoop periodically removes expired pending auths and codes.
func (s *simpleService) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.cleanup()
		case <-s.stopCh:
			return
		}
	}
}

func (s *simpleService) cleanup() {
	now := time.Now()

	s.pendingMu.Lock()
	for key, p := range s.pending {
		if now.Sub(p.CreatedAt) > authCodeTTL {
			delete(s.pending, key)
		}
	}
	s.pendingMu.Unlock()

	s.codesMu.Lock()
	for key, c := range s.codes {
		if now.Sub(c.CreatedAt) > authCodeTTL || c.Used {
			delete(s.codes, key)
		}
	}
	s.codesMu.Unlock()

	s.devicesMu.Lock()
	for key, d := range s.devices {
		if now.After(d.ExpiresAt) {
			delete(s.userCodes, normalizeUserCode(d.UserCode))
			delete(s.devices, key)
		}
	}
	s.devicesMu.Unlock()
}

// handleResourceMetadata returns RFC 9728 protected resource metadata.
func (s *simpleService) handleResourceMetadata(w http.ResponseWriter, r *http.Request) {
	baseURL := baseURLFromRequest(r)

	metadata := map[string]any{
		"resource":                 baseURL,
		"authorization_servers":    []string{baseURL},
		"bearer_methods_supported": []string{"header"},
		"scopes_supported":         []string{"mcp"},
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "max-age=3600")
	_ = json.NewEncoder(w).Encode(metadata)
}

// handleServerMetadata returns RFC 8414 authorization server metadata.
func (s *simpleService) handleServerMetadata(w http.ResponseWriter, r *http.Request) {
	baseURL := baseURLFromRequest(r)

	metadata := map[string]any{
		"issuer":                                baseURL,
		"authorization_endpoint":                baseURL + "/auth/authorize",
		"token_endpoint":                        baseURL + "/auth/token",
		"device_authorization_endpoint":         baseURL + "/auth/device/code",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token", "urn:ietf:params:oauth:grant-type:device_code"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{"mcp", "offline_access"},
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "max-age=3600")
	_ = json.NewEncoder(w).Encode(metadata)
}

// handleAuthorize starts the OAuth flow.
func (s *simpleService) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	// Validate required parameters.
	clientID := q.Get("client_id")
	redirectURI := q.Get("redirect_uri")
	codeChallenge := q.Get("code_challenge")
	codeChallengeMethod := q.Get("code_challenge_method")
	resource := q.Get("resource")
	state := q.Get("state")

	if codeChallengeMethod != "S256" {
		s.writeError(w, http.StatusBadRequest, "invalid_request", "code_challenge_method must be S256")
		return
	}

	if codeChallenge == "" {
		s.writeError(w, http.StatusBadRequest, "invalid_request", "code_challenge is required")
		return
	}

	if resource == "" {
		s.writeError(w, http.StatusBadRequest, "invalid_request", "resource is required (RFC 8707)")
		return
	}

	if redirectURI == "" {
		s.writeError(w, http.StatusBadRequest, "invalid_request", "redirect_uri is required")
		return
	}

	// Validate redirect URI.
	if !github.ValidateRedirectURI(redirectURI) {
		s.writeError(w, http.StatusBadRequest, "invalid_request", "invalid redirect_uri")
		return
	}

	// Generate state for GitHub.
	githubState, err := s.generateState()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "server_error", "failed to generate state")
		return
	}

	// Store pending authorization.
	s.pendingMu.Lock()
	s.pending[githubState] = &pendingAuth{
		ClientID:      clientID,
		RedirectURI:   redirectURI,
		CodeChallenge: codeChallenge,
		Resource:      resource,
		State:         state,
		CreatedAt:     time.Now(),
	}
	s.pendingMu.Unlock()

	// Redirect to GitHub.
	baseURL := baseURLFromRequest(r)
	callbackURL := baseURL + "/auth/callback"
	githubURL := s.github.GetAuthorizationURL(callbackURL, githubState, "read:user read:org")

	s.log.WithField("client_id", clientID).Info("Starting auth flow")
	http.Redirect(w, r, githubURL, http.StatusFound)
}

// handleCallback handles the GitHub OAuth callback.
// Supports both PKCE flow (redirect to client) and device flow (mark device as approved).
func (s *simpleService) handleCallback(w http.ResponseWriter, r *http.Request) { //nolint:funlen,cyclop // auth callback with two flow branches
	ctx := r.Context()
	q := r.URL.Query()

	code := q.Get("code")
	state := q.Get("state")

	if q.Get("error") != "" {
		s.writeHTMLError(w, http.StatusBadRequest, "Authentication Failed", q.Get("error_description"))
		return
	}

	if code == "" || state == "" {
		s.writeHTMLError(w, http.StatusBadRequest, "Error", "missing code or state")
		return
	}

	// Get pending authorization.
	s.pendingMu.Lock()
	pending, ok := s.pending[state]
	if ok {
		delete(s.pending, state)
	}
	s.pendingMu.Unlock()

	if !ok {
		s.writeHTMLError(w, http.StatusBadRequest, "Error", "invalid or expired state")
		return
	}

	// Exchange code for GitHub token.
	baseURL := baseURLFromRequest(r)
	callbackURL := baseURL + "/auth/callback"
	githubToken, err := s.github.ExchangeCode(ctx, code, callbackURL)
	if err != nil {
		s.log.WithError(err).Error("GitHub code exchange failed")
		s.writeHTMLError(w, http.StatusBadRequest, "Authentication Failed", err.Error())
		return
	}

	// Get GitHub user.
	githubUser, err := s.github.GetUser(ctx, githubToken.AccessToken)
	if err != nil {
		s.log.WithError(err).Error("Failed to get GitHub user")
		s.writeHTMLError(w, http.StatusInternalServerError, "Error", "failed to get user profile")
		return
	}

	// Validate org membership.
	if len(s.allowedOrgs) > 0 && !githubUser.IsMemberOf(s.allowedOrgs) {
		s.log.WithFields(logrus.Fields{
			"login":        githubUser.Login,
			"user_orgs":    githubUser.Organizations,
			"allowed_orgs": s.allowedOrgs,
		}).Warn("User not in allowed organizations")
		s.writeHTMLError(w, http.StatusForbidden, "Access Denied",
			"You are not authorized to access this resource.")
		return
	}

	// Device flow: mark device auth as approved instead of issuing an authorization code.
	if pending.DeviceCode != "" {
		s.devicesMu.Lock()
		dev, ok := s.devices[pending.DeviceCode]

		if !ok {
			s.devicesMu.Unlock()
			s.writeHTMLError(w, http.StatusBadRequest, "Error", "device authorization has expired, please try again")

			return
		}

		dev.Authorized = true
		dev.GitHubLogin = githubUser.Login
		dev.GitHubID = githubUser.ID
		dev.Orgs = githubUser.Organizations
		s.devicesMu.Unlock()

		s.log.WithFields(logrus.Fields{
			"login":       githubUser.Login,
			"device_code": pending.DeviceCode,
		}).Info("Device authorization approved")

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(buildDeviceSuccessPage(githubUser.Login)))

		return
	}

	// PKCE flow: generate authorization code and redirect back to client.
	codeStr, err := s.generateState()
	if err != nil {
		s.writeHTMLError(w, http.StatusInternalServerError, "Error", "failed to generate code")
		return
	}

	// Store authorization code.
	s.codesMu.Lock()
	s.codes[codeStr] = &issuedCode{
		Code:          codeStr,
		ClientID:      pending.ClientID,
		RedirectURI:   pending.RedirectURI,
		Resource:      pending.Resource,
		CodeChallenge: pending.CodeChallenge,
		GitHubLogin:   githubUser.Login,
		GitHubID:      githubUser.ID,
		Orgs:          githubUser.Organizations,
		CreatedAt:     time.Now(),
	}
	s.codesMu.Unlock()

	s.log.WithFields(logrus.Fields{
		"login":     githubUser.Login,
		"client_id": pending.ClientID,
	}).Info("Authorization successful")

	// Redirect back to client with user info for the success page.
	redirectParams := url.Values{"code": {codeStr}}
	if pending.State != "" {
		redirectParams.Set("state", pending.State)
	}

	redirectParams.Set("login", githubUser.Login)
	redirectParams.Set("avatar_url", githubUser.AvatarURL)

	if len(githubUser.Organizations) > 0 {
		redirectParams.Set("orgs", strings.Join(githubUser.Organizations, ","))
	}

	// Resolve success page display customization from config rules.
	if s.cfg.SuccessPage != nil {
		display := s.cfg.SuccessPage.Resolve(githubUser.Login, githubUser.Organizations)
		if display.Tagline != "" {
			redirectParams.Set("sp_tagline", display.Tagline)
		}

		if display.Media != nil {
			redirectParams.Set("sp_media_type", display.Media.Type)

			if display.Media.URL != "" {
				redirectParams.Set("sp_media_url", display.Media.URL)
			}

			if display.Media.ASCIIArtBase64 != "" {
				redirectParams.Set("sp_media_ascii", display.Media.ASCIIArtBase64)
			}
		}
	}

	redirectURL := fmt.Sprintf("%s?%s", pending.RedirectURI, redirectParams.Encode())
	http.Redirect(w, r, redirectURL, http.StatusFound)
}

// handleToken exchanges an authorization code for a bearer token.
func (s *simpleService) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", "invalid form data")
		return
	}

	grantType := r.FormValue("grant_type")
	switch grantType {
	case "authorization_code":
		s.handleAuthorizationCodeGrant(w, r)
	case "refresh_token":
		s.handleRefreshTokenGrant(w, r)
	case "urn:ietf:params:oauth:grant-type:device_code":
		s.handleDeviceTokenGrant(w, r)
	default:
		s.writeError(w, http.StatusBadRequest, "unsupported_grant_type",
			"supported grant types are authorization_code, refresh_token, and device_code")
	}
}

func (s *simpleService) handleAuthorizationCodeGrant(w http.ResponseWriter, r *http.Request) {
	code := r.FormValue("code")
	redirectURI := r.FormValue("redirect_uri")
	clientID := r.FormValue("client_id")
	codeVerifier := r.FormValue("code_verifier")
	resource := r.FormValue("resource")

	if code == "" || codeVerifier == "" || resource == "" {
		s.writeError(w, http.StatusBadRequest, "invalid_request", "missing required parameters")
		return
	}

	// Get and validate authorization code.
	// All validation must complete before marking as used to prevent:
	// 1. Replay attacks (can't reuse a code)
	// 2. DoS attacks (attacker can't burn a stolen code with invalid params)
	s.codesMu.Lock()
	issued, ok := s.codes[code]

	if !ok {
		s.codesMu.Unlock()
		s.writeError(w, http.StatusBadRequest, "invalid_grant", "invalid authorization code")

		return
	}

	// Check if already used or expired before marking as used.
	if issued.Used {
		s.codesMu.Unlock()
		s.writeError(w, http.StatusBadRequest, "invalid_grant", "authorization code already used")

		return
	}

	if time.Since(issued.CreatedAt) > authCodeTTL {
		s.codesMu.Unlock()
		s.writeError(w, http.StatusBadRequest, "invalid_grant", "authorization code expired")

		return
	}

	// Validate all parameters before marking as used to prevent DoS attacks
	// where an attacker could burn a stolen code with invalid parameters.
	if issued.ClientID != clientID || issued.RedirectURI != redirectURI || issued.Resource != resource {
		s.codesMu.Unlock()
		s.writeError(w, http.StatusBadRequest, "invalid_grant", "parameter mismatch")

		return
	}

	// Verify PKCE before marking as used.
	if !s.verifyPKCE(codeVerifier, issued.CodeChallenge) {
		s.codesMu.Unlock()
		s.writeError(w, http.StatusBadRequest, "invalid_grant", "invalid code_verifier")

		return
	}

	// Mark as used only after all checks pass.
	issued.Used = true
	s.codesMu.Unlock()

	baseURL := baseURLFromRequest(r)

	accessToken, err := s.issueAccessToken(baseURL, issued.Resource, issued.GitHubLogin, issued.GitHubID, issued.Orgs)
	if err != nil {
		s.log.WithError(err).Error("Failed to sign token")
		s.writeError(w, http.StatusInternalServerError, "server_error", "failed to create token")
		return
	}

	refreshToken, err := s.issueRefreshToken(
		baseURL, issued.ClientID, issued.Resource, issued.GitHubLogin, issued.GitHubID, issued.Orgs,
	)
	if err != nil {
		s.log.WithError(err).Error("Failed to create refresh token")
		s.writeError(w, http.StatusInternalServerError, "server_error", "failed to create refresh token")
		return
	}

	s.log.WithFields(logrus.Fields{
		"login":     issued.GitHubLogin,
		"client_id": clientID,
	}).Info("Token issued")

	s.writeTokenResponse(w, accessToken, refreshToken)
}

func (s *simpleService) handleRefreshTokenGrant(w http.ResponseWriter, r *http.Request) {
	refreshToken := r.FormValue("refresh_token")
	clientID := r.FormValue("client_id")
	resource := r.FormValue("resource")

	if refreshToken == "" || clientID == "" {
		s.writeError(w, http.StatusBadRequest, "invalid_request", "missing required parameters")
		return
	}

	baseURL := baseURLFromRequest(r)

	// Validate the stateless refresh token JWT.
	claims := &refreshTokenClaims{}
	token, err := jwt.ParseWithClaims(refreshToken, claims, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return s.secretKey, nil
	}, jwt.WithIssuer(baseURL), jwt.WithExpirationRequired())

	if err != nil || !token.Valid {
		s.writeError(w, http.StatusBadRequest, "invalid_grant", "invalid refresh token")
		return
	}

	if claims.TokenType != refreshTokenType {
		s.writeError(w, http.StatusBadRequest, "invalid_grant", "invalid refresh token")
		return
	}

	if claims.ClientID != clientID {
		s.writeError(w, http.StatusBadRequest, "invalid_grant", "parameter mismatch")
		return
	}

	if resource != "" && claims.Resource != resource {
		s.writeError(w, http.StatusBadRequest, "invalid_grant", "parameter mismatch")
		return
	}

	accessToken, err := s.issueAccessToken(baseURL, claims.Resource, claims.GitHubLogin, claims.GitHubID, claims.Orgs)
	if err != nil {
		s.log.WithError(err).Error("Failed to sign refreshed token")
		s.writeError(w, http.StatusInternalServerError, "server_error", "failed to create token")
		return
	}

	// Return the same refresh token — it's stateless and still valid.
	s.writeTokenResponse(w, accessToken, refreshToken)
}

// handleDeviceCode handles POST /auth/device/code (RFC 8628 device authorization request).
func (s *simpleService) handleDeviceCode(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", "invalid form data")
		return
	}

	clientID := r.FormValue("client_id")
	resource := r.FormValue("resource")

	if clientID == "" {
		s.writeError(w, http.StatusBadRequest, "invalid_request", "client_id is required")
		return
	}

	if resource == "" {
		s.writeError(w, http.StatusBadRequest, "invalid_request", "resource is required")
		return
	}

	deviceCode, err := s.generateRandomToken(32)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "server_error", "failed to generate device code")
		return
	}

	userCode, err := s.generateUniqueUserCode()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "server_error", "failed to generate user code")
		return
	}

	now := time.Now()
	dev := &deviceAuth{
		DeviceCode: deviceCode,
		UserCode:   userCode,
		ClientID:   clientID,
		Resource:   resource,
		CreatedAt:  now,
		ExpiresAt:  now.Add(deviceCodeTTL),
	}

	s.devicesMu.Lock()
	s.devices[deviceCode] = dev
	s.userCodes[normalizeUserCode(userCode)] = deviceCode
	s.devicesMu.Unlock()

	baseURL := baseURLFromRequest(r)

	s.log.WithFields(logrus.Fields{
		"client_id": clientID,
		"user_code": userCode,
	}).Info("Device authorization started")

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"device_code":      deviceCode,
		"user_code":        userCode,
		"verification_uri": baseURL + "/auth/device",
		"expires_in":       int(deviceCodeTTL.Seconds()),
		"interval":         devicePollInterval,
	})
}

// handleDevicePage handles GET /auth/device (browser page to enter user code).
func (s *simpleService) handleDevicePage(w http.ResponseWriter, r *http.Request) {
	userCode := r.URL.Query().Get("code")

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(buildDevicePage(userCode, "")))
}

// handleDeviceVerify handles POST /auth/device/verify (user code form submission).
func (s *simpleService) handleDeviceVerify(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(buildDevicePage("", "Invalid request.")))

		return
	}

	userCode := r.FormValue("user_code")
	normalized := normalizeUserCode(userCode)

	if normalized == "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(buildDevicePage("", "Please enter a code.")))

		return
	}

	// Look up device auth by user code and copy needed fields while holding the lock.
	s.devicesMu.RLock()
	deviceCode, ok := s.userCodes[normalized]

	var (
		valid       bool
		devClient   string
		devResource string
	)

	if ok {
		if dev := s.devices[deviceCode]; dev != nil && !dev.Authorized && time.Now().Before(dev.ExpiresAt) {
			valid = true
			devClient = dev.ClientID
			devResource = dev.Resource
		}
	}

	s.devicesMu.RUnlock()

	if !valid {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(buildDevicePage(userCode, "Invalid or expired code. Check the code in your terminal and try again.")))

		return
	}

	// Create a pending auth that links to this device code.
	githubState, err := s.generateState()
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(buildDevicePage(userCode, "Something went wrong. Please try again.")))

		return
	}

	s.pendingMu.Lock()
	s.pending[githubState] = &pendingAuth{
		ClientID:   devClient,
		Resource:   devResource,
		CreatedAt:  time.Now(),
		DeviceCode: deviceCode,
	}
	s.pendingMu.Unlock()

	// Redirect to GitHub for authentication.
	baseURL := baseURLFromRequest(r)
	callbackURL := baseURL + "/auth/callback"
	githubURL := s.github.GetAuthorizationURL(callbackURL, githubState, "read:user read:org")

	s.log.WithFields(logrus.Fields{
		"user_code":   userCode,
		"device_code": deviceCode,
	}).Info("Device verification started, redirecting to GitHub")

	http.Redirect(w, r, githubURL, http.StatusFound)
}

// handleDeviceTokenGrant handles the device_code grant type in POST /auth/token.
func (s *simpleService) handleDeviceTokenGrant(w http.ResponseWriter, r *http.Request) {
	deviceCode := r.FormValue("device_code")
	clientID := r.FormValue("client_id")

	if deviceCode == "" || clientID == "" {
		s.writeError(w, http.StatusBadRequest, "invalid_request", "missing required parameters")
		return
	}

	s.devicesMu.Lock()
	dev, ok := s.devices[deviceCode]

	if !ok {
		s.devicesMu.Unlock()
		s.writeError(w, http.StatusBadRequest, "invalid_grant", "invalid device code")

		return
	}

	if time.Now().After(dev.ExpiresAt) {
		delete(s.userCodes, normalizeUserCode(dev.UserCode))
		delete(s.devices, deviceCode)
		s.devicesMu.Unlock()
		s.writeError(w, http.StatusBadRequest, "expired_token", "device code has expired")

		return
	}

	if dev.ClientID != clientID {
		s.devicesMu.Unlock()
		s.writeError(w, http.StatusBadRequest, "invalid_grant", "client_id mismatch")

		return
	}

	if !dev.Authorized {
		s.devicesMu.Unlock()
		s.writeError(w, http.StatusBadRequest, "authorization_pending", "waiting for user to authorize")

		return
	}

	// Consume the device auth — tokens are issued once.
	login := dev.GitHubLogin
	ghID := dev.GitHubID
	orgs := dev.Orgs
	resource := dev.Resource

	delete(s.userCodes, normalizeUserCode(dev.UserCode))
	delete(s.devices, deviceCode)
	s.devicesMu.Unlock()

	baseURL := baseURLFromRequest(r)

	accessToken, err := s.issueAccessToken(baseURL, resource, login, ghID, orgs)
	if err != nil {
		s.log.WithError(err).Error("Failed to sign device token")
		s.writeError(w, http.StatusInternalServerError, "server_error", "failed to create token")

		return
	}

	refreshToken, err := s.issueRefreshToken(baseURL, clientID, resource, login, ghID, orgs)
	if err != nil {
		s.log.WithError(err).Error("Failed to create device refresh token")
		s.writeError(w, http.StatusInternalServerError, "server_error", "failed to create refresh token")

		return
	}

	s.log.WithFields(logrus.Fields{
		"login":     login,
		"client_id": clientID,
	}).Info("Device token issued")

	s.writeTokenResponse(w, accessToken, refreshToken)
}

func (s *simpleService) issueAccessToken(
	issuerURL, resource, githubLogin string, githubID int64, orgs []string,
) (string, error) {
	now := time.Now()
	claims := &tokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    issuerURL,
			Subject:   fmt.Sprintf("%d", githubID),
			Audience:  jwt.ClaimStrings{resource},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(accessTokenTTL)),
		},
		GitHubLogin: githubLogin,
		GitHubID:    githubID,
		Orgs:        orgs,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(s.secretKey)
}

func (s *simpleService) issueRefreshToken(
	issuerURL string,
	clientID string,
	resource string,
	githubLogin string,
	githubID int64,
	orgs []string,
) (string, error) {
	now := time.Now()
	claims := &refreshTokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    issuerURL,
			Subject:   fmt.Sprintf("%d", githubID),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(refreshTokenTTL)),
		},
		GitHubLogin: githubLogin,
		GitHubID:    githubID,
		Orgs:        append([]string(nil), orgs...),
		ClientID:    clientID,
		Resource:    resource,
		TokenType:   refreshTokenType,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(s.secretKey)
}

func (s *simpleService) writeTokenResponse(w http.ResponseWriter, accessToken, refreshToken string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")

	response := map[string]any{
		"access_token": accessToken,
		"token_type":   "Bearer",
		"expires_in":   int(accessTokenTTL.Seconds()),
	}
	if refreshToken != "" {
		response["refresh_token"] = refreshToken
	}

	_ = json.NewEncoder(w).Encode(response)
}

func (s *simpleService) generateState() (string, error) {
	return s.generateRandomToken(32)
}

func (s *simpleService) generateRandomToken(size int) (string, error) {
	bytes := make([]byte, size)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

// generateUserCode creates a random user code in XXXX-XXXX format using consonants only.
func (s *simpleService) generateUserCode() (string, error) {
	b := make([]byte, userCodeHalfLen*2)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating random bytes: %w", err)
	}

	code := make([]byte, userCodeHalfLen*2)
	for i := range code {
		code[i] = userCodeAlphabet[int(b[i])%len(userCodeAlphabet)]
	}

	return string(code[:userCodeHalfLen]) + "-" + string(code[userCodeHalfLen:]), nil
}

// generateUniqueUserCode generates a user code that doesn't collide with existing codes.
func (s *simpleService) generateUniqueUserCode() (string, error) {
	const maxRetries = 5

	for range maxRetries {
		code, err := s.generateUserCode()
		if err != nil {
			return "", err
		}

		s.devicesMu.RLock()
		_, exists := s.userCodes[normalizeUserCode(code)]
		s.devicesMu.RUnlock()

		if !exists {
			return code, nil
		}
	}

	return "", fmt.Errorf("failed to generate unique user code after %d attempts", maxRetries)
}

// normalizeUserCode strips hyphens/spaces and uppercases a user code for comparison.
func normalizeUserCode(code string) string {
	code = strings.ToUpper(code)
	code = strings.ReplaceAll(code, "-", "")
	code = strings.ReplaceAll(code, " ", "")

	return code
}

func (s *simpleService) verifyPKCE(verifier, challenge string) bool {
	hash := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(hash[:])
	return computed == challenge
}

func (s *simpleService) writeError(w http.ResponseWriter, status int, errCode, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":             errCode,
		"error_description": description,
	})
}

func (s *simpleService) writeHTMLError(w http.ResponseWriter, status int, title, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `<!DOCTYPE html><html><head><title>%s</title></head><body><h1>%s</h1><p>%s</p></body></html>`,
		html.EscapeString(title), html.EscapeString(title), html.EscapeString(message))
}

func (s *simpleService) writeUnauthorized(w http.ResponseWriter, baseURL, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(
		`Bearer resource_metadata="%s/.well-known/oauth-protected-resource", error="invalid_token", error_description="%s"`,
		baseURL, description))
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":             "invalid_token",
		"error_description": description,
	})
}

// baseURLFromRequest derives the external base URL from the incoming request's
// Host header and TLS state. Behind a reverse proxy that sets X-Forwarded-Proto
// and X-Forwarded-Host, those headers take precedence.
func baseURLFromRequest(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}

	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	}

	host := r.Host
	if fwdHost := r.Header.Get("X-Forwarded-Host"); fwdHost != "" {
		host = fwdHost
	}

	return strings.TrimSuffix(fmt.Sprintf("%s://%s", scheme, host), "/")
}
