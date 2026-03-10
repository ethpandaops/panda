// Package auth provides simplified GitHub-based OAuth for local product edges.
//
// This implements a minimal OAuth 2.1 authorization server that:
// - Delegates identity verification to GitHub
// - Issues signed bearer tokens with proper resource (audience) binding per RFC 8707
// - Validates bearer tokens on protected endpoints
//
// The flow is:
// 1. Client calls /auth/authorize with resource + PKCE
// 2. Server redirects to GitHub for authentication
// 3. GitHub redirects back to /auth/callback
// 4. Server verifies org membership, issues authorization code
// 5. Client exchanges code for JWT at /auth/token
// 6. Client uses JWT to access MCP endpoints
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

	"github.com/ethpandaops/mcp/pkg/auth/github"
)

const (
	// Authorization code TTL.
	authCodeTTL = 5 * time.Minute

	// Access token TTL.
	accessTokenTTL = 1 * time.Hour
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
	baseURL     string
	github      *github.Client
	secretKey   []byte
	allowedOrgs []string

	// Pending authorization requests (state -> pendingAuth).
	pending   map[string]*pendingAuth
	pendingMu sync.RWMutex

	// Authorization codes (code -> issuedCode).
	codes   map[string]*issuedCode
	codesMu sync.RWMutex

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

// tokenClaims are the JWT claims.
type tokenClaims struct {
	jwt.RegisteredClaims
	GitHubLogin string   `json:"github_login"`
	GitHubID    int64    `json:"github_id"`
	Orgs        []string `json:"orgs,omitempty"`
}

// NewSimpleService creates a new simplified auth service.
func NewSimpleService(log logrus.FieldLogger, cfg Config, baseURL string) (SimpleService, error) {
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
		baseURL:     strings.TrimSuffix(baseURL, "/"),
		github:      github.NewClient(log, cfg.GitHub.ClientID, cfg.GitHub.ClientSecret),
		secretKey:   []byte(cfg.Tokens.SecretKey),
		allowedOrgs: cfg.AllowedOrgs,
		pending:     make(map[string]*pendingAuth),
		codes:       make(map[string]*issuedCode),
		stopCh:      make(chan struct{}),
	}

	log.WithFields(logrus.Fields{
		"base_url":     baseURL,
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
}

// handleResourceMetadata returns RFC 9728 protected resource metadata.
func (s *simpleService) handleResourceMetadata(w http.ResponseWriter, _ *http.Request) {
	metadata := map[string]any{
		"resource":                 s.baseURL,
		"authorization_servers":    []string{s.baseURL},
		"bearer_methods_supported": []string{"header"},
		"scopes_supported":         []string{"mcp"},
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "max-age=3600")
	_ = json.NewEncoder(w).Encode(metadata)
}

// handleServerMetadata returns RFC 8414 authorization server metadata.
func (s *simpleService) handleServerMetadata(w http.ResponseWriter, _ *http.Request) {
	metadata := map[string]any{
		"issuer":                                s.baseURL,
		"authorization_endpoint":                s.baseURL + "/auth/authorize",
		"token_endpoint":                        s.baseURL + "/auth/token",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{"mcp"},
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
	callbackURL := s.baseURL + "/auth/callback"
	githubURL := s.github.GetAuthorizationURL(callbackURL, githubState, "read:user read:org")

	s.log.WithField("client_id", clientID).Info("Starting auth flow")
	http.Redirect(w, r, githubURL, http.StatusFound)
}

// handleCallback handles the GitHub OAuth callback.
func (s *simpleService) handleCallback(w http.ResponseWriter, r *http.Request) {
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
	callbackURL := s.baseURL + "/auth/callback"
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

	// Generate authorization code.
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

	// Redirect back to client.
	redirectParams := url.Values{"code": {codeStr}}
	if pending.State != "" {
		redirectParams.Set("state", pending.State)
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
	if grantType != "authorization_code" {
		s.writeError(w, http.StatusBadRequest, "unsupported_grant_type", "only authorization_code is supported")
		return
	}

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

	// Create signed bearer token.
	now := time.Now()
	claims := &tokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.baseURL,
			Subject:   fmt.Sprintf("%d", issued.GitHubID),
			Audience:  jwt.ClaimStrings{issued.Resource},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(accessTokenTTL)),
		},
		GitHubLogin: issued.GitHubLogin,
		GitHubID:    issued.GitHubID,
		Orgs:        issued.Orgs,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	accessToken, err := token.SignedString(s.secretKey)
	if err != nil {
		s.log.WithError(err).Error("Failed to sign token")
		s.writeError(w, http.StatusInternalServerError, "server_error", "failed to create token")
		return
	}

	s.log.WithFields(logrus.Fields{
		"login":     issued.GitHubLogin,
		"client_id": clientID,
	}).Info("Token issued")

	// Return token response.
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": accessToken,
		"token_type":   "Bearer",
		"expires_in":   int(accessTokenTTL.Seconds()),
	})
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
				s.writeUnauthorized(w, "missing or invalid Authorization header")
				return
			}

			tokenStr := strings.TrimPrefix(authHeader, "Bearer ")

			// Validate token.
			claims := &tokenClaims{}
			token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
				if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
					return nil, fmt.Errorf("unexpected signing method")
				}
				return s.secretKey, nil
			}, jwt.WithIssuer(s.baseURL), jwt.WithExpirationRequired())

			if err != nil || !token.Valid {
				s.writeUnauthorized(w, "invalid token")
				return
			}

			// Validate audience (RFC 8707).
			audienceValid := false
			for _, aud := range claims.Audience {
				if aud == s.baseURL {
					audienceValid = true
					break
				}
			}
			if !audienceValid {
				s.writeUnauthorized(w, "token audience mismatch")
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

type authUserKeyType string

const authUserKey authUserKeyType = "auth_user"

// GetAuthUser returns the authenticated user from context.
func GetAuthUser(ctx context.Context) *AuthUser {
	user, _ := ctx.Value(authUserKey).(*AuthUser)
	return user
}

// Helper functions.

func (s *simpleService) generateState() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
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

func (s *simpleService) writeUnauthorized(w http.ResponseWriter, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(
		`Bearer resource_metadata="%s/.well-known/oauth-protected-resource", error="invalid_token", error_description="%s"`,
		s.baseURL, description))
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":             "invalid_token",
		"error_description": description,
	})
}
