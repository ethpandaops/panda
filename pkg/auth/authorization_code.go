package auth

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/auth/github"
)

// handleAuthorize starts the OAuth flow.
func (s *authorizationServer) handleAuthorize(w http.ResponseWriter, r *http.Request) {
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

	if !s.isExpectedResource(resource) {
		s.writeError(w, http.StatusBadRequest, "invalid_target", "unsupported resource")
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
	baseURL := s.issuerURL
	callbackURL := baseURL + "/auth/callback"
	githubURL := s.github.GetAuthorizationURL(callbackURL, githubState, "read:user read:org")

	s.log.WithField("client_id", clientID).Info("Starting auth flow")
	http.Redirect(w, r, githubURL, http.StatusFound)
}

// handleCallback handles the GitHub OAuth callback.
// Supports both PKCE flow (redirect to client) and device flow (mark device as approved).
func (s *authorizationServer) handleCallback(w http.ResponseWriter, r *http.Request) { //nolint:funlen,cyclop // auth callback with two flow branches
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
	baseURL := s.issuerURL
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
		dev.GitHubToken = githubToken.AccessToken
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
		GitHubToken:   githubToken.AccessToken,
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
func (s *authorizationServer) handleToken(w http.ResponseWriter, r *http.Request) {
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

func (s *authorizationServer) handleAuthorizationCodeGrant(w http.ResponseWriter, r *http.Request) {
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

	baseURL := s.issuerURL

	accessToken, err := s.issueAccessToken(baseURL, issued.Resource, issued.GitHubLogin, issued.GitHubID, issued.Orgs)
	if err != nil {
		s.log.WithError(err).Error("Failed to sign token")
		s.writeError(w, http.StatusInternalServerError, "server_error", "failed to create token")
		return
	}

	refreshToken, err := s.issueRefreshToken(
		issued.ClientID,
		issued.Resource,
		issued.GitHubLogin,
		issued.GitHubID,
		issued.GitHubToken,
		issued.Orgs,
	)
	if err != nil {
		s.log.WithError(err).Error("Failed to create refresh session")
		s.writeError(w, http.StatusInternalServerError, "server_error", "failed to create refresh token")
		return
	}

	s.log.WithFields(logrus.Fields{
		"login":     issued.GitHubLogin,
		"client_id": clientID,
	}).Info("Token issued")

	s.writeTokenResponse(w, accessToken, refreshToken)
}

func (s *authorizationServer) handleRefreshTokenGrant(w http.ResponseWriter, r *http.Request) {
	refreshToken := r.FormValue("refresh_token")
	clientID := r.FormValue("client_id")
	resource := r.FormValue("resource")

	if refreshToken == "" || clientID == "" {
		s.writeError(w, http.StatusBadRequest, "invalid_request", "missing required parameters")
		return
	}

	// Copy every field we need out of the session while the lock is held, and
	// never touch the *refreshSession pointer again afterward. A concurrent
	// request presenting the same token can rotate or delete it at any point
	// once we release the lock, so any later read through the pointer would
	// race that request's writes to the same object.
	s.refreshSessionsMu.RLock()
	session, ok := s.refreshSessions[refreshToken]
	var (
		sessionClientID   string
		sessionResource   string
		sessionGitHubID   int64
		sessionGitHub     string
		sessionGitHubUser string
		sessionOrgs       []string
		sessionExpiresAt  time.Time
	)
	if ok {
		sessionClientID = session.ClientID
		sessionResource = session.Resource
		sessionGitHubID = session.GitHubID
		sessionGitHub = session.GitHubAccessToken
		sessionGitHubUser = session.GitHubLogin
		sessionOrgs = append([]string(nil), session.Orgs...)
		sessionExpiresAt = session.ExpiresAt
	}
	s.refreshSessionsMu.RUnlock()

	if !ok {
		s.writeError(w, http.StatusBadRequest, "invalid_grant", "invalid refresh token")
		return
	}

	if time.Now().After(sessionExpiresAt) {
		s.refreshSessionsMu.Lock()
		delete(s.refreshSessions, refreshToken)
		s.refreshSessionsMu.Unlock()
		s.writeError(w, http.StatusBadRequest, "invalid_grant", "refresh token expired")
		return
	}

	if sessionClientID != clientID {
		s.writeError(w, http.StatusBadRequest, "invalid_grant", "parameter mismatch")
		return
	}

	if resource != "" && sessionResource != resource {
		s.writeError(w, http.StatusBadRequest, "invalid_grant", "parameter mismatch")
		return
	}

	githubToken := sessionGitHub
	githubLogin := sessionGitHubUser
	githubID := sessionGitHubID
	orgs := sessionOrgs

	if len(s.allowedOrgs) > 0 {
		githubUser, err := s.github.GetUser(r.Context(), sessionGitHub)
		if err != nil {
			s.log.WithError(err).WithField("login", sessionGitHubUser).Warn("Failed to verify GitHub org membership during refresh")
			s.writeError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "could not verify organization membership")
			return
		}

		if githubUser.ID != sessionGitHubID {
			s.refreshSessionsMu.Lock()
			delete(s.refreshSessions, refreshToken)
			s.refreshSessionsMu.Unlock()
			s.writeError(w, http.StatusBadRequest, "invalid_grant", "refresh token subject mismatch")
			return
		}

		if !githubUser.IsMemberOf(s.allowedOrgs) {
			s.refreshSessionsMu.Lock()
			delete(s.refreshSessions, refreshToken)
			s.refreshSessionsMu.Unlock()
			s.writeError(w, http.StatusBadRequest, "invalid_grant", "user no longer belongs to an allowed organization")
			return
		}

		githubLogin = githubUser.Login
		orgs = append([]string(nil), githubUser.Organizations...)
	}

	accessToken, err := s.issueAccessToken(s.issuerURL, sessionResource, githubLogin, githubID, orgs)
	if err != nil {
		s.log.WithError(err).Error("Failed to sign refreshed token")
		s.writeError(w, http.StatusInternalServerError, "server_error", "failed to create token")
		return
	}

	newRefreshToken, err := s.rotateRefreshToken(refreshToken, sessionClientID, sessionResource, githubLogin, githubID, githubToken, orgs)
	if err != nil {
		if errors.Is(err, errRefreshTokenAlreadyConsumed) {
			s.writeError(w, http.StatusBadRequest, "invalid_grant", "refresh token already used")
			return
		}

		s.log.WithError(err).Error("Failed to rotate refresh session")
		s.writeError(w, http.StatusInternalServerError, "server_error", "failed to rotate refresh token")
		return
	}

	s.writeTokenResponse(w, accessToken, newRefreshToken)
}
