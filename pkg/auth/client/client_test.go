package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
)

func TestDiscoverFallsBackToOAuthAuthorizationServer(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			http.NotFound(w, r)
		case "/.well-known/oauth-authorization-server":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":                 "http://example.test",
				"authorization_endpoint": "http://example.test/auth/authorize",
				"token_endpoint":         "http://example.test/auth/token",
				"scopes_supported":       []string{"mcp"},
			})
		default:
			t.Fatalf("unexpected discovery path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	c := New(logrus.New(), Config{
		IssuerURL: server.URL,
		ClientID:  "panda",
	}).(*client)

	if err := c.discover(context.Background()); err != nil {
		t.Fatalf("discover failed: %v", err)
	}

	if c.oidc == nil {
		t.Fatal("expected discovery metadata")
	}

	if c.oidc.AuthorizationEndpoint != "http://example.test/auth/authorize" {
		t.Fatalf("unexpected authorization endpoint: %s", c.oidc.AuthorizationEndpoint)
	}

	if c.oidc.TokenEndpoint != "http://example.test/auth/token" {
		t.Fatalf("unexpected token endpoint: %s", c.oidc.TokenEndpoint)
	}
}

func TestBuildAuthURLOmitsResourceWhenEmpty(t *testing.T) {
	t.Parallel()

	c := New(logrus.New(), Config{
		IssuerURL: "https://issuer.example.com",
		ClientID:  "panda-proxy",
	}).(*client)
	c.oidc = &OIDCConfig{AuthorizationEndpoint: "https://issuer.example.com/auth"}

	authURL := c.buildAuthURL("state-123", "challenge-123", "http://localhost:8085/callback")
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if got := parsed.Query().Get("resource"); got != "" {
		t.Fatalf("expected no resource parameter, got %q", got)
	}

	scope := parsed.Query().Get("scope")
	if !strings.Contains(scope, "offline_access") {
		t.Fatalf("expected default scopes to include offline_access, got %q", scope)
	}
}

func TestBearerTokenFromResponsePrefersIDToken(t *testing.T) {
	t.Parallel()

	resp := tokenResponse{
		AccessToken: "access-token",
		IDToken:     "id-token",
	}

	if got := bearerTokenFromResponse(resp); got != "id-token" {
		t.Fatalf("expected id_token to be preferred, got %q", got)
	}
}
