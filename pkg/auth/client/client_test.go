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

func TestClientCredentialsMintsToken(t *testing.T) {
	t.Parallel()

	var gotForm url.Values

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":         server.URL,
				"token_endpoint": server.URL + "/token",
			})
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parsing form: %v", err)
			}

			gotForm = r.PostForm
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "svc-access-token",
				"token_type":   "Bearer",
				"expires_in":   3600,
			})
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	c := New(logrus.New(), Config{
		IssuerURL: server.URL,
		ClientID:  "panda-proxy",
		Username:  "panda-chat-svc",
		Password:  "app-password",
	})

	tokens, err := c.ClientCredentials(context.Background())
	if err != nil {
		t.Fatalf("ClientCredentials failed: %v", err)
	}

	if tokens.AccessToken != "svc-access-token" {
		t.Fatalf("unexpected access token: %q", tokens.AccessToken)
	}

	if tokens.RefreshToken != "" {
		t.Fatalf("client_credentials must not carry a refresh token, got %q", tokens.RefreshToken)
	}

	if tokens.ExpiresAt.IsZero() {
		t.Fatal("expected ExpiresAt to be set")
	}

	for key, want := range map[string]string{
		"grant_type": "client_credentials",
		"client_id":  "panda-proxy",
		"username":   "panda-chat-svc",
		"password":   "app-password",
	} {
		if got := gotForm.Get(key); got != want {
			t.Errorf("form %s = %q, want %q", key, got, want)
		}
	}

	if scope := gotForm.Get("scope"); strings.Contains(scope, "offline_access") {
		t.Errorf("scope must not include offline_access, got %q", scope)
	}
}

func TestClientCredentialsRequiresCredentials(t *testing.T) {
	t.Parallel()

	c := New(logrus.New(), Config{
		IssuerURL: "http://example.test",
		ClientID:  "panda-proxy",
	})

	if _, err := c.ClientCredentials(context.Background()); err == nil {
		t.Fatal("expected error when username/password are missing")
	}
}

func TestClientCredentialsSurfacesTokenEndpointError(t *testing.T) {
	t.Parallel()

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":         server.URL,
				"token_endpoint": server.URL + "/token",
			})
		case "/token":
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusUnauthorized)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	c := New(logrus.New(), Config{
		IssuerURL: server.URL,
		ClientID:  "panda-proxy",
		Username:  "panda-chat-svc",
		Password:  "wrong",
	})

	_, err := c.ClientCredentials(context.Background())
	if err == nil {
		t.Fatal("expected error from token endpoint")
	}

	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected 401 in error, got: %v", err)
	}
}

func TestRequestDeviceCodeRequestsOfflineAccessScope(t *testing.T) {
	t.Parallel()

	var gotForm url.Values

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":                        server.URL,
				"token_endpoint":                server.URL + "/token",
				"device_authorization_endpoint": server.URL + "/device",
			})
		case "/device":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parsing form: %v", err)
			}

			gotForm = r.PostForm
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_code":      "device-code",
				"user_code":        "USER-CODE",
				"verification_uri": server.URL + "/activate",
				"expires_in":       600,
				"interval":         5,
			})
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	c, ok := New(logrus.New(), Config{
		IssuerURL: server.URL,
		ClientID:  "panda-proxy",
	}).(*client)
	if !ok {
		t.Fatal("New did not return *client")
	}

	if err := c.discover(context.Background()); err != nil {
		t.Fatalf("discover failed: %v", err)
	}

	if _, err := c.requestDeviceCode(context.Background()); err != nil {
		t.Fatalf("requestDeviceCode failed: %v", err)
	}

	if got := gotForm.Get("client_id"); got != "panda-proxy" {
		t.Errorf("form client_id = %q, want %q", got, "panda-proxy")
	}

	// offline_access must be requested in the device authorization flow so the
	// provider issues a refresh token; without it, headless sessions cannot
	// auto-refresh and must re-run the full device flow on every expiry.
	if scope := gotForm.Get("scope"); !strings.Contains(scope, "offline_access") {
		t.Fatalf("device authorization scope must include offline_access, got %q", scope)
	}
}
