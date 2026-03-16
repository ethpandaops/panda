package client

import (
	"context"
	"encoding/base64"
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

func TestParseIDTokenClaims(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		token    string
		wantUser string
		wantMail string
		wantGrps []string
	}{
		{
			name: "valid JWT with all claims",
			token: makeTestJWT(t, map[string]any{
				"preferred_username": "alice",
				"email":              "alice@example.com",
				"groups":             []string{"devops", "sre"},
			}),
			wantUser: "alice",
			wantMail: "alice@example.com",
			wantGrps: []string{"devops", "sre"},
		},
		{
			name: "missing preferred_username falls back empty",
			token: makeTestJWT(t, map[string]any{
				"email": "bob@example.com",
			}),
			wantUser: "",
			wantMail: "bob@example.com",
		},
		{
			name:  "invalid JWT returns zero claims",
			token: "not-a-jwt",
		},
		{
			name:  "malformed base64 payload returns zero claims",
			token: "header.!!!invalid-base64!!!.sig",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			claims := parseIDTokenClaims(tt.token)

			if claims.PreferredUsername != tt.wantUser {
				t.Errorf("PreferredUsername = %q, want %q", claims.PreferredUsername, tt.wantUser)
			}

			if claims.Email != tt.wantMail {
				t.Errorf("Email = %q, want %q", claims.Email, tt.wantMail)
			}

			if len(tt.wantGrps) > 0 {
				if len(claims.Groups) != len(tt.wantGrps) {
					t.Fatalf("Groups length = %d, want %d", len(claims.Groups), len(tt.wantGrps))
				}

				for i, g := range claims.Groups {
					if g != tt.wantGrps[i] {
						t.Errorf("Groups[%d] = %q, want %q", i, g, tt.wantGrps[i])
					}
				}
			}
		})
	}
}

// makeTestJWT builds a minimal unsigned JWT with the given claims payload.
func makeTestJWT(t *testing.T, claims map[string]any) string {
	t.Helper()

	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))

	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshaling claims: %v", err)
	}

	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)

	return header + "." + encodedPayload + ".nosig"
}

func TestFetchBrandingReturnsNilWithoutURL(t *testing.T) {
	t.Parallel()

	c := New(logrus.New(), Config{
		IssuerURL: "https://issuer.example.com",
		ClientID:  "panda",
	}).(*client)

	got := c.fetchBranding(context.Background())
	if got != nil {
		t.Fatalf("expected nil branding when BrandingURL is empty, got %+v", got)
	}
}

func TestFetchBrandingReturnsConfig(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"default": map[string]any{
				"tagline": "Welcome!",
			},
		})
	}))
	defer srv.Close()

	c := New(logrus.New(), Config{
		IssuerURL:   "https://issuer.example.com",
		ClientID:    "panda",
		BrandingURL: srv.URL + "/auth/branding",
	}).(*client)

	got := c.fetchBranding(context.Background())
	if got == nil {
		t.Fatal("expected branding config, got nil")
	}

	if got.Default == nil || got.Default.Tagline != "Welcome!" {
		t.Fatalf("unexpected default tagline: %+v", got.Default)
	}
}
