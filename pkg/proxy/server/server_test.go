package proxyserver

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"

	simpleauth "github.com/ethpandaops/panda/pkg/auth"
)

func TestRegisterRoutesMatchesClickHouseSubpaths(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		ClickHouse: []ClickHouseClusterConfig{
			{
				Name:     "xatu",
				Host:     "example.com",
				Port:     8123,
				Username: "user",
				Password: "pass",
			},
		},
	}
	cfg.ApplyDefaults()

	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	if err != nil {
		t.Fatalf("newServer failed: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/clickhouse/query", nil)
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected clickhouse handler status %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestNewServerRejectsInvalidLokiConfig(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		Loki: []LokiInstanceConfig{
			{
				Name: "broken",
				URL:  "not-a-url",
			},
		},
	}
	cfg.ApplyDefaults()

	_, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	if err == nil {
		t.Fatal("expected newServer to fail")
	}
}

func TestDatasourcesRouteAllowsUnauthenticatedRequestsWhenAuthDisabled(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
	}
	cfg.ApplyDefaults()

	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	if err != nil {
		t.Fatalf("newServer failed: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/datasources", nil)
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected datasources status %d, got %d", http.StatusOK, rec.Code)
	}
}

func TestDatasourcesRouteRejectsUnauthorizedOAuthRequests(t *testing.T) {
	t.Parallel()

	logger, hook := logrustest.NewNullLogger()
	cfg := oauthTestServerConfig()

	srv, err := newServer(logger, cfg, "http://proxy.test", "18081")
	if err != nil {
		t.Fatalf("newServer failed: %v", err)
	}
	defer stopMiddlewareTestServer(t, srv)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/datasources", nil)
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected datasources status %d, got %d", http.StatusUnauthorized, rec.Code)
	}

	if got := rec.Header().Get("WWW-Authenticate"); got == "" {
		t.Fatal("expected WWW-Authenticate header on unauthorized response")
	}

	if got := len(auditEntries(hook.AllEntries())); got != 0 {
		t.Fatalf("unexpected audit log count = %d, want 0", got)
	}
}

func TestDatasourcesRoutePropagatesIdentityThroughRateLimitAndAuditMiddleware(t *testing.T) {
	t.Parallel()

	logger, hook := logrustest.NewNullLogger()
	logger.SetLevel(logrus.InfoLevel)

	cfg := oauthTestServerConfig()
	cfg.RateLimiting = RateLimitConfig{
		Enabled:           true,
		RequestsPerMinute: 1,
		BurstSize:         1,
	}
	cfg.Audit = AuditConfig{
		Enabled:        true,
		MaxQueryLength: 256,
	}
	cfg.ApplyDefaults()

	srv, err := newServer(logger, cfg, "http://proxy.test", "18081")
	if err != nil {
		t.Fatalf("newServer failed: %v", err)
	}
	defer stopMiddlewareTestServer(t, srv)

	token := issueOAuthAccessToken(t, "http://proxy.test", cfg.Auth.Tokens.SecretKey, 42, "octocat")

	first := httptest.NewRecorder()
	firstReq := httptest.NewRequest(http.MethodGet, "/datasources", nil)
	firstReq.Header.Set("Authorization", "Bearer "+token)
	srv.mux.ServeHTTP(first, firstReq)

	if first.Code != http.StatusOK {
		t.Fatalf("expected first datasources status %d, got %d", http.StatusOK, first.Code)
	}

	second := httptest.NewRecorder()
	secondReq := httptest.NewRequest(http.MethodGet, "/datasources", nil)
	secondReq.Header.Set("Authorization", "Bearer "+token)
	srv.mux.ServeHTTP(second, secondReq)

	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("expected second datasources status %d, got %d", http.StatusTooManyRequests, second.Code)
	}

	if got := second.Header().Get("Retry-After"); got != "60" {
		t.Fatalf("Retry-After = %q, want %q", got, "60")
	}

	audits := auditEntries(hook.AllEntries())
	if len(audits) != 1 {
		t.Fatalf("audit log count = %d, want 1", len(audits))
	}

	audit := audits[0]
	if got := audit.Data["user_id"]; got != "42" {
		t.Fatalf("audit user_id = %#v, want %q", got, "42")
	}

	if got := audit.Data["path"]; got != "/datasources" {
		t.Fatalf("audit path = %#v, want %q", got, "/datasources")
	}

	if got := audit.Data["status"]; got != http.StatusOK {
		t.Fatalf("audit status = %#v, want %d", got, http.StatusOK)
	}
}

func oauthTestServerConfig() ServerConfig {
	cfg := ServerConfig{
		Auth: AuthConfig{
			Mode: AuthModeOAuth,
			GitHub: &simpleauth.GitHubConfig{
				ClientID:     "client-id",
				ClientSecret: "client-secret",
			},
			Tokens: simpleauth.TokensConfig{
				SecretKey: "test-secret-key",
			},
		},
	}
	cfg.ApplyDefaults()

	return cfg
}

func issueOAuthAccessToken(t *testing.T, issuer, secret string, githubID int64, githubLogin string) string {
	t.Helper()

	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss":          issuer,
		"sub":          fmt.Sprintf("%d", githubID),
		"aud":          []string{issuer},
		"iat":          now.Unix(),
		"exp":          now.Add(time.Hour).Unix(),
		"github_login": githubLogin,
		"github_id":    githubID,
		"orgs":         []string{"ethpandaops"},
	})

	tokenStr, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("signed token: %v", err)
	}

	return tokenStr
}

func auditEntries(entries []*logrus.Entry) []*logrus.Entry {
	filtered := make([]*logrus.Entry, 0, len(entries))
	for _, entry := range entries {
		if entry.Message == "Audit" {
			filtered = append(filtered, entry)
		}
	}

	return filtered
}

func stopMiddlewareTestServer(t *testing.T, srv *server) {
	t.Helper()

	if srv.rateLimiter != nil {
		srv.rateLimiter.Stop()
	}

	if srv.authenticator != nil {
		if err := srv.authenticator.Stop(); err != nil {
			t.Fatalf("stopping authenticator: %v", err)
		}
	}
}
