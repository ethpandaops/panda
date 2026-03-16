package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/auth/github"
)

const testIssuerURL = "https://proxy.example.com"

func TestHandleTokenIssuesAccessAndRefreshTokens(t *testing.T) {
	t.Parallel()

	svc := newTestSimpleService(t, nil)
	verifier := "verifier-123"
	challenge := sha256.Sum256([]byte(verifier))
	svc.codes["auth-code"] = &issuedCode{
		Code:          "auth-code",
		ClientID:      "panda",
		RedirectURI:   "http://localhost:8085/callback",
		Resource:      testIssuerURL,
		CodeChallenge: base64.RawURLEncoding.EncodeToString(challenge[:]),
		GitHubLogin:   "sam",
		GitHubID:      42,
		GitHubToken:   "github-access-token",
		Orgs:          []string{"ethpandaops"},
		CreatedAt:     time.Now(),
	}

	resp := exchangeToken(t, svc, "http://internal-proxy/auth/token", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {"auth-code"},
		"redirect_uri":  {"http://localhost:8085/callback"},
		"client_id":     {"panda"},
		"code_verifier": {verifier},
		"resource":      {testIssuerURL},
	})

	if resp.AccessToken == "" {
		t.Fatal("expected access token in authorization_code response")
	}

	if resp.RefreshToken == "" {
		t.Fatal("expected refresh token in authorization_code response")
	}

	claims := parseAccessTokenClaims(t, resp.AccessToken)
	if claims.Issuer != testIssuerURL {
		t.Fatalf("expected issuer %q, got %q", testIssuerURL, claims.Issuer)
	}
	if len(claims.Audience) != 1 || claims.Audience[0] != testIssuerURL {
		t.Fatalf("expected audience %q, got %#v", testIssuerURL, claims.Audience)
	}

	svc.refreshSessionsMu.RLock()
	session := svc.refreshSessions[resp.RefreshToken]
	svc.refreshSessionsMu.RUnlock()
	if session == nil {
		t.Fatal("expected refresh session to be stored")
	}
	if session.GitHubAccessToken != "github-access-token" {
		t.Fatalf("expected refresh session to retain GitHub access token, got %q", session.GitHubAccessToken)
	}
}

func TestMiddlewareUsesConfiguredIssuerURLInsteadOfRequestHost(t *testing.T) {
	t.Parallel()

	svc := newTestSimpleService(t, nil)
	token, err := svc.issueAccessToken(testIssuerURL, testIssuerURL, "sam", 42, nil)
	if err != nil {
		t.Fatalf("issueAccessToken failed: %v", err)
	}

	handler := svc.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "http://internal-proxy/clickhouse/query", nil)
	req.Host = "internal-proxy"
	req.Header.Set("X-Forwarded-Host", "attacker.example.com")
	req.Header.Set("X-Forwarded-Proto", "http")
	req.Header.Set("Authorization", "Bearer "+token)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d: %s", http.StatusNoContent, rec.Code, rec.Body.String())
	}
}

func TestHandleTokenRefreshGrantRevalidatesOrgMembership(t *testing.T) {
	t.Parallel()

	svc := newTestSimpleService(t, []string{"ethpandaops"})
	stubGitHub := &stubGitHubClient{
		user: &github.GitHubUser{
			ID:            42,
			Login:         "sam",
			Organizations: []string{"ethpandaops", "sigp"},
		},
	}
	svc.github = stubGitHub

	refreshToken, err := svc.issueRefreshToken(
		"panda",
		testIssuerURL,
		"sam",
		42,
		"github-access-token",
		[]string{"stale-org"},
	)
	if err != nil {
		t.Fatalf("issueRefreshToken failed: %v", err)
	}

	svc.refreshSessionsMu.RLock()
	initialSession := svc.refreshSessions[refreshToken]
	svc.refreshSessionsMu.RUnlock()
	if initialSession == nil {
		t.Fatal("expected initial refresh session to be stored")
	}
	initialExpiry := initialSession.ExpiresAt

	resp := exchangeToken(t, svc, "http://internal-proxy/auth/token", url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {"panda"},
		"resource":      {testIssuerURL},
	})

	if resp.AccessToken == "" {
		t.Fatal("expected access token in refresh_token response")
	}
	if resp.RefreshToken == "" {
		t.Fatal("expected rotated refresh token in refresh_token response")
	}
	if resp.RefreshToken == refreshToken {
		t.Fatal("expected refresh token rotation, but token was reused")
	}
	if stubGitHub.getUserCalls != 1 {
		t.Fatalf("expected 1 GitHub membership lookup, got %d", stubGitHub.getUserCalls)
	}

	claims := parseAccessTokenClaims(t, resp.AccessToken)
	if len(claims.Orgs) != 2 || claims.Orgs[0] != "ethpandaops" || claims.Orgs[1] != "sigp" {
		t.Fatalf("expected refreshed org claims, got %#v", claims.Orgs)
	}

	svc.refreshSessionsMu.RLock()
	oldSession := svc.refreshSessions[refreshToken]
	session := svc.refreshSessions[resp.RefreshToken]
	svc.refreshSessionsMu.RUnlock()
	if oldSession != nil {
		t.Fatal("expected old refresh session to be revoked after rotation")
	}
	if session == nil {
		t.Fatal("expected refresh session to remain valid")
	}
	if len(session.Orgs) != 2 || session.Orgs[0] != "ethpandaops" || session.Orgs[1] != "sigp" {
		t.Fatalf("expected refresh session orgs to update, got %#v", session.Orgs)
	}
	if !session.ExpiresAt.After(initialExpiry) {
		t.Fatalf("expected rotated refresh session expiry to advance beyond %s, got %s", initialExpiry, session.ExpiresAt)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://internal-proxy/auth/token", strings.NewReader(url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {"panda"},
		"resource":      {testIssuerURL},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	svc.handleToken(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected old refresh token to fail after rotation, got %d", rec.Code)
	}
}

func TestHandleTokenRefreshGrantRejectsRemovedOrg(t *testing.T) {
	t.Parallel()

	svc := newTestSimpleService(t, []string{"ethpandaops"})
	svc.github = &stubGitHubClient{
		user: &github.GitHubUser{
			ID:            42,
			Login:         "sam",
			Organizations: []string{"someone-else"},
		},
	}

	refreshToken, err := svc.issueRefreshToken(
		"panda",
		testIssuerURL,
		"sam",
		42,
		"github-access-token",
		[]string{"ethpandaops"},
	)
	if err != nil {
		t.Fatalf("issueRefreshToken failed: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://internal-proxy/auth/token", strings.NewReader(url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {"panda"},
		"resource":      {testIssuerURL},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	svc.handleToken(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "user no longer belongs to an allowed organization") {
		t.Fatalf("expected org membership rejection, got %s", rec.Body.String())
	}

	svc.refreshSessionsMu.RLock()
	session := svc.refreshSessions[refreshToken]
	svc.refreshSessionsMu.RUnlock()
	if session != nil {
		t.Fatal("expected refresh session to be revoked after org membership loss")
	}
}

type tokenResponseBody struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

type stubGitHubClient struct {
	user         *github.GitHubUser
	getUserCalls int
}

func (s *stubGitHubClient) GetAuthorizationURL(redirectURI, state, scope string) string {
	return "https://github.example.test/oauth"
}

func (s *stubGitHubClient) ExchangeCode(_ context.Context, code, redirectURI string) (*github.TokenResponse, error) {
	return &github.TokenResponse{AccessToken: "github-access-token"}, nil
}

func (s *stubGitHubClient) GetUser(_ context.Context, accessToken string) (*github.GitHubUser, error) {
	s.getUserCalls++
	return s.user, nil
}

func exchangeToken(t *testing.T, svc *simpleService, targetURL string, values url.Values) tokenResponseBody {
	t.Helper()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, targetURL, strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	svc.handleToken(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", rec.Code, rec.Body.String())
	}

	var body tokenResponseBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	return body
}

func parseAccessTokenClaims(t *testing.T, tokenString string) *tokenClaims {
	t.Helper()

	claims := &tokenClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		return []byte("test-secret"), nil
	})
	if err != nil || !token.Valid {
		t.Fatalf("failed to parse access token: %v", err)
	}

	return claims
}

func newTestSimpleService(t *testing.T, allowedOrgs []string) *simpleService {
	t.Helper()

	service, err := NewSimpleService(logrus.New(), Config{
		Enabled:     true,
		IssuerURL:   testIssuerURL,
		AllowedOrgs: append([]string(nil), allowedOrgs...),
		GitHub: &GitHubConfig{
			ClientID:     "github-client",
			ClientSecret: "github-secret",
		},
		Tokens: TokensConfig{SecretKey: "test-secret"},
	})
	if err != nil {
		t.Fatalf("NewSimpleService failed: %v", err)
	}

	return service.(*simpleService)
}
