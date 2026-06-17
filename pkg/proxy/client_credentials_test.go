package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sirupsen/logrus"
)

// fakeIssuer is a minimal OIDC issuer serving discovery and a
// client_credentials token endpoint. Each mint returns "svc-token-<n>".
type fakeIssuer struct {
	server    *httptest.Server
	mints     atomic.Int64
	expiresIn int
}

func newFakeIssuer(t *testing.T, expiresIn int) *fakeIssuer {
	t.Helper()

	issuer := &fakeIssuer{expiresIn: expiresIn}

	issuer.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":         issuer.server.URL,
				"token_endpoint": issuer.server.URL + "/token",
			})
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parsing token form: %v", err)
			}

			if got := r.PostForm.Get("grant_type"); got != "client_credentials" {
				t.Errorf("grant_type = %q, want client_credentials", got)
			}

			n := issuer.mints.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": fmt.Sprintf("svc-token-%d", n),
				"token_type":   "Bearer",
				"expires_in":   issuer.expiresIn,
			})
		default:
			http.NotFound(w, r)
		}
	}))

	t.Cleanup(issuer.server.Close)

	return issuer
}

// newFakeProxy serves /datasources, accepting only bearer tokens for which
// accept returns true and replying 401 otherwise.
func newFakeProxy(t *testing.T, accept func(token string) bool) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/datasources" {
			http.NotFound(w, r)
			return
		}

		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !accept(token) {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"clickhouse": []string{"xatu-experimental"},
		})
	}))

	t.Cleanup(server.Close)

	return server
}

func newClientCredentialsClient(issuerURL, proxyURL string) *proxyClient {
	c := NewClient(logrus.New(), ClientConfig{
		URL:       proxyURL,
		IssuerURL: issuerURL,
		ClientID:  "panda-proxy",
		AuthMode:  AuthModeClientCredentials,
		Username:  "panda-chat-svc",
		Password:  "app-password",
	})

	return c.(*proxyClient)
}

func TestClientCredentialsTokenIsCachedInMemory(t *testing.T) {
	t.Parallel()

	issuer := newFakeIssuer(t, 3600)
	client := newClientCredentialsClient(issuer.server.URL, "http://unused.test")

	first := client.RegisterToken()
	if first != "svc-token-1" {
		t.Fatalf("RegisterToken() = %q, want svc-token-1", first)
	}

	second := client.RegisterToken()
	if second != first {
		t.Fatalf("expected cached token %q, got %q", first, second)
	}

	if got := issuer.mints.Load(); got != 1 {
		t.Fatalf("expected 1 mint for cached token, got %d", got)
	}
}

func TestClientCredentialsReMintsExpiredToken(t *testing.T) {
	t.Parallel()

	// expires_in of 1s is inside the refresh buffer, so every call re-mints.
	issuer := newFakeIssuer(t, 1)
	client := newClientCredentialsClient(issuer.server.URL, "http://unused.test")

	first := client.RegisterToken()
	second := client.RegisterToken()

	if first == "" || second == "" {
		t.Fatalf("expected tokens, got %q then %q", first, second)
	}

	if first == second {
		t.Fatalf("expected a re-mint across expiry, got the same token %q twice", first)
	}

	if got := issuer.mints.Load(); got != 2 {
		t.Fatalf("expected 2 mints, got %d", got)
	}
}

func TestClientCredentialsDiscoverRetriesOn401(t *testing.T) {
	t.Parallel()

	issuer := newFakeIssuer(t, 3600)

	// The proxy rejects the first minted token (e.g. revoked server-side)
	// and accepts the second.
	proxy := newFakeProxy(t, func(token string) bool {
		return token == "svc-token-2"
	})

	client := newClientCredentialsClient(issuer.server.URL, proxy.URL)

	// Warm the cache with the soon-to-be-rejected token.
	if got := client.RegisterToken(); got != "svc-token-1" {
		t.Fatalf("RegisterToken() = %q, want svc-token-1", got)
	}

	if err := client.Discover(context.Background()); err != nil {
		t.Fatalf("Discover failed after re-mint retry: %v", err)
	}

	if got := issuer.mints.Load(); got != 2 {
		t.Fatalf("expected 2 mints (initial + retry), got %d", got)
	}

	if names := client.ClickHouseDatasources(); len(names) != 1 || names[0] != "xatu-experimental" {
		t.Fatalf("unexpected datasources after retry: %v", names)
	}
}

func TestClientCredentialsDiscoverFailsWhenRetryRejected(t *testing.T) {
	t.Parallel()

	issuer := newFakeIssuer(t, 3600)

	// The proxy rejects every token: the single retry must not loop.
	proxy := newFakeProxy(t, func(string) bool { return false })

	client := newClientCredentialsClient(issuer.server.URL, proxy.URL)

	err := client.Discover(context.Background())
	if err == nil {
		t.Fatal("expected Discover to fail when the proxy rejects all tokens")
	}

	if got := issuer.mints.Load(); got != 2 {
		t.Fatalf("expected exactly 2 mints (no retry loop), got %d", got)
	}
}

// Serving a cached client_credentials token across a mint outage while the
// token is inside the refresh buffer is covered by the token package's
// clientCredentialsSource tests, where the in-memory cache is directly
// controllable.

func TestClickHouseQueryRetriesOn401(t *testing.T) {
	t.Parallel()

	issuer := newFakeIssuer(t, 3600)

	var calls atomic.Int64

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/clickhouse/" {
			http.NotFound(w, r)
			return
		}

		// Reject the first token (revoked server-side), accept the re-mint.
		if calls.Add(1) == 1 {
			http.Error(w, "revoked", http.StatusUnauthorized)
			return
		}

		_, _ = w.Write([]byte("query-ok"))
	}))
	t.Cleanup(proxy.Close)

	client := newClientCredentialsClient(issuer.server.URL, proxy.URL)

	body, err := client.ClickHouseQuery(context.Background(), "xatu", "SELECT 1", nil)
	if err != nil {
		t.Fatalf("ClickHouseQuery error after retry: %v", err)
	}

	if string(body) != "query-ok" {
		t.Fatalf("body = %q, want query-ok", body)
	}

	if got := calls.Load(); got != 2 {
		t.Fatalf("expected 2 clickhouse attempts (initial + retry), got %d", got)
	}

	if got := issuer.mints.Load(); got != 2 {
		t.Fatalf("expected 2 mints (initial + re-mint on 401), got %d", got)
	}
}

func TestReadyFlipsAfterSuccessfulDiscovery(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/datasources" {
			http.NotFound(w, r)
			return
		}

		_ = json.NewEncoder(w).Encode(map[string]any{"clickhouse": []string{"xatu"}})
	}))
	t.Cleanup(srv.Close)

	// No auth configured (local-proxy style): discovery should just succeed.
	client := NewClient(logrus.New(), ClientConfig{URL: srv.URL}).(*proxyClient)

	if client.Ready() {
		t.Fatal("Ready() should be false before the first discovery")
	}

	if err := client.Discover(context.Background()); err != nil {
		t.Fatalf("Discover error = %v", err)
	}

	if !client.Ready() {
		t.Fatal("Ready() should be true after a successful discovery")
	}
}

func TestReadyTrueWhenBlockedOnAuth(t *testing.T) {
	t.Parallel()

	// The proxy rejects every token, so discovery can never succeed without the
	// user (re)authenticating. The server is up and can do no better unattended,
	// so it must report ready rather than wedge the init/start wait at 503.
	issuer := newFakeIssuer(t, 3600)
	proxy := newFakeProxy(t, func(string) bool { return false })

	client := newClientCredentialsClient(issuer.server.URL, proxy.URL)

	if client.Ready() {
		t.Fatal("Ready() should be false before any discovery attempt")
	}

	if err := client.Discover(context.Background()); err == nil {
		t.Fatal("expected discovery to fail with authentication required")
	}

	if !client.Ready() {
		t.Fatal("Ready() should be true when discovery is blocked waiting for auth")
	}
}
