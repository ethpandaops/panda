package auth

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/auth/github"
)

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
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
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

	if !github.ValidateRedirectURI(redirectURI) {
		s.writeError(w, http.StatusBadRequest, "invalid_request", "invalid redirect_uri")
		return
	}

	githubState, err := s.generateState()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "server_error", "failed to generate state")
		return
	}

	s.storePendingAuth(githubState, &pendingAuth{
		ClientID:      clientID,
		RedirectURI:   redirectURI,
		CodeChallenge: codeChallenge,
		Resource:      resource,
		State:         state,
		CreatedAt:     time.Now(),
	})

	callbackURL := baseURLFromRequest(r) + "/auth/callback"
	githubURL := s.github.GetAuthorizationURL(callbackURL, githubState, "read:user read:org")

	s.log.WithField("client_id", clientID).Info("Starting auth flow")
	http.Redirect(w, r, githubURL, http.StatusFound)
}

// handleCallback handles the GitHub OAuth callback.
func (s *simpleService) handleCallback(w http.ResponseWriter, r *http.Request) {
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

	pending, ok := s.takePendingAuth(state)
	if !ok {
		s.writeHTMLError(w, http.StatusBadRequest, "Error", "invalid or expired state")
		return
	}

	callbackURL := baseURLFromRequest(r) + "/auth/callback"
	githubToken, err := s.github.ExchangeCode(r.Context(), code, callbackURL)
	if err != nil {
		s.log.WithError(err).Error("GitHub code exchange failed")
		s.writeHTMLError(w, http.StatusBadRequest, "Authentication Failed", err.Error())
		return
	}

	githubUser, err := s.github.GetUser(r.Context(), githubToken.AccessToken)
	if err != nil {
		s.log.WithError(err).Error("Failed to get GitHub user")
		s.writeHTMLError(w, http.StatusInternalServerError, "Error", "failed to get user profile")
		return
	}

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

	codeStr, err := s.generateState()
	if err != nil {
		s.writeHTMLError(w, http.StatusInternalServerError, "Error", "failed to generate code")
		return
	}

	s.storeIssuedCode(codeStr, &issuedCode{
		Code:          codeStr,
		ClientID:      pending.ClientID,
		RedirectURI:   pending.RedirectURI,
		Resource:      pending.Resource,
		CodeChallenge: pending.CodeChallenge,
		GitHubLogin:   githubUser.Login,
		GitHubID:      githubUser.ID,
		Orgs:          githubUser.Organizations,
		CreatedAt:     time.Now(),
	})

	s.log.WithFields(logrus.Fields{
		"login":     githubUser.Login,
		"client_id": pending.ClientID,
	}).Info("Authorization successful")

	redirectParams := url.Values{"code": {codeStr}}
	if pending.State != "" {
		redirectParams.Set("state", pending.State)
	}

	redirectParams.Set("login", githubUser.Login)
	redirectParams.Set("avatar_url", githubUser.AvatarURL)

	if len(githubUser.Organizations) > 0 {
		redirectParams.Set("orgs", strings.Join(githubUser.Organizations, ","))
	}

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

	switch r.FormValue("grant_type") {
	case "authorization_code":
		s.handleAuthorizationCodeGrant(w, r)
	case "refresh_token":
		s.handleRefreshTokenGrant(w, r)
	default:
		s.writeError(w, http.StatusBadRequest, "unsupported_grant_type",
			"supported grant types are authorization_code and refresh_token")
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

	issued, errDescription := s.consumeAuthorizationCode(code, clientID, redirectURI, resource, codeVerifier)
	if errDescription != "" {
		s.writeError(w, http.StatusBadRequest, "invalid_grant", errDescription)
		return
	}

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

	claims, errDescription := s.validateRefreshToken(baseURL, refreshToken, clientID, resource)
	if errDescription != "" {
		s.writeError(w, http.StatusBadRequest, "invalid_grant", errDescription)
		return
	}

	accessToken, err := s.issueAccessToken(baseURL, claims.Resource, claims.GitHubLogin, claims.GitHubID, claims.Orgs)
	if err != nil {
		s.log.WithError(err).Error("Failed to sign refreshed token")
		s.writeError(w, http.StatusInternalServerError, "server_error", "failed to create token")
		return
	}

	s.writeTokenResponse(w, accessToken, refreshToken)
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
