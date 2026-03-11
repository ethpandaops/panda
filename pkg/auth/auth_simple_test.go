package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

func TestHandleTokenIssuesRefreshTokenAndSupportsRefreshGrant(t *testing.T) {
	t.Parallel()

	service, err := NewSimpleService(logrus.New(), Config{
		Enabled: true,
		GitHub: &GitHubConfig{
			ClientID:     "github-client",
			ClientSecret: "github-secret",
		},
		Tokens: TokensConfig{SecretKey: "test-secret"},
	}, "http://proxy.test")
	if err != nil {
		t.Fatalf("NewSimpleService failed: %v", err)
	}

	svc := service.(*simpleService)
	verifier := "verifier-123"
	challenge := sha256.Sum256([]byte(verifier))
	svc.codes["auth-code"] = &issuedCode{
		Code:          "auth-code",
		ClientID:      "panda",
		RedirectURI:   "http://localhost:8085/callback",
		Resource:      "http://proxy.test",
		CodeChallenge: base64.RawURLEncoding.EncodeToString(challenge[:]),
		GitHubLogin:   "sam",
		GitHubID:      42,
		CreatedAt:     time.Now(),
	}

	authorizationResponse := exchangeToken(t, svc, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {"auth-code"},
		"redirect_uri":  {"http://localhost:8085/callback"},
		"client_id":     {"panda"},
		"code_verifier": {verifier},
		"resource":      {"http://proxy.test"},
	})

	if authorizationResponse.AccessToken == "" {
		t.Fatal("expected access token in authorization_code response")
	}

	if authorizationResponse.RefreshToken == "" {
		t.Fatal("expected refresh token in authorization_code response")
	}

	refreshResponse := exchangeToken(t, svc, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {authorizationResponse.RefreshToken},
		"client_id":     {"panda"},
		"resource":      {"http://proxy.test"},
	})

	if refreshResponse.AccessToken == "" {
		t.Fatal("expected access token in refresh_token response")
	}

	if refreshResponse.RefreshToken != authorizationResponse.RefreshToken {
		t.Fatalf("expected refresh token to be preserved, got %q", refreshResponse.RefreshToken)
	}
}

type tokenResponseBody struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

func exchangeToken(t *testing.T, svc *simpleService, values url.Values) tokenResponseBody {
	t.Helper()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/token", strings.NewReader(values.Encode()))
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
