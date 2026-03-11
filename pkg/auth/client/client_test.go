package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
