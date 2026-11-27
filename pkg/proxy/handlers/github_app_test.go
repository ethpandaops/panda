package handlers

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testAppID          = 12345
	testInstallationID = 67890
)

// fakeGitHub records the requests a GitHub API fake receives.
type fakeGitHub struct {
	mu           sync.Mutex
	tokenCalls   int
	tokenAuth    []string
	tokenBodies  []map[string]any
	apiAuth      []string
	tokenExpires time.Time
}

func (f *fakeGitHub) handler(t *testing.T) http.Handler {
	t.Helper()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		if r.Method == http.MethodPost && r.URL.Path == fmt.Sprintf("/app/installations/%d/access_tokens", testInstallationID) {
			var body map[string]any
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))

			f.tokenCalls++
			f.tokenAuth = append(f.tokenAuth, r.Header.Get("Authorization"))
			f.tokenBodies = append(f.tokenBodies, body)

			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":      fmt.Sprintf("ghs_installation_%d", f.tokenCalls),
				"expires_at": f.tokenExpires.UTC().Format(time.RFC3339),
			})

			return
		}

		f.apiAuth = append(f.apiAuth, r.Header.Get("Authorization"))

		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/dispatches"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/runs"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"workflow_runs": []map[string]any{{
					"id":         42,
					"status":     "queued",
					"html_url":   "https://github.com/run/42",
					"created_at": time.Now().UTC().Add(time.Minute).Format(time.RFC3339),
				}},
			})
		default:
			http.NotFound(w, r)
		}
	})
}

// locked runs fn while holding the fake's lock, so tests can read and change
// its state without racing the server goroutine.
func (f *fakeGitHub) locked(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()

	fn()
}

func newTestAppKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	return key
}

func newTestTokenSource(t *testing.T, key *rsa.PrivateKey, apiBase string) *githubAppTokenSource {
	t.Helper()

	return newGitHubAppTokenSource(
		GitHubAppConfig{AppID: testAppID, InstallationID: testInstallationID, PrivateKey: key},
		apiBase,
		[]string{"eth-client-docker-image-builder"},
		map[string]string{"actions": "write"},
		&http.Client{},
	)
}

func TestGitHubAppTokenSourceSignsJWTWithAppKey(t *testing.T) {
	t.Parallel()

	key := newTestAppKey(t)
	fake := &fakeGitHub{tokenExpires: time.Now().Add(time.Hour)}
	srv := httptest.NewServer(fake.handler(t))
	t.Cleanup(srv.Close)

	_, err := newTestTokenSource(t, key, srv.URL).Token(context.Background())
	require.NoError(t, err)

	var authHeader string

	fake.locked(func() {
		require.Len(t, fake.tokenAuth, 1)
		authHeader = fake.tokenAuth[0]
	})

	raw, ok := strings.CutPrefix(authHeader, "Bearer ")
	require.True(t, ok)

	claims := &jwt.RegisteredClaims{}
	parsed, err := jwt.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) {
		return &key.PublicKey, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}))
	require.NoError(t, err)
	require.True(t, parsed.Valid)

	assert.Equal(t, "12345", claims.Issuer)
	require.NotNil(t, claims.ExpiresAt)
	require.NotNil(t, claims.IssuedAt)
	assert.LessOrEqual(t, claims.ExpiresAt.Sub(claims.IssuedAt.Time), 10*time.Minute)

	otherKey := newTestAppKey(t)
	_, err = jwt.Parse(raw, func(token *jwt.Token) (any, error) {
		return &otherKey.PublicKey, nil
	})
	assert.Error(t, err, "JWT must not verify against a different key")
}

func TestGitHubAppTokenSourceSendsScope(t *testing.T) {
	t.Parallel()

	fake := &fakeGitHub{tokenExpires: time.Now().Add(time.Hour)}
	srv := httptest.NewServer(fake.handler(t))
	t.Cleanup(srv.Close)

	_, err := newTestTokenSource(t, newTestAppKey(t), srv.URL).Token(context.Background())
	require.NoError(t, err)

	fake.locked(func() {
		require.Len(t, fake.tokenBodies, 1)
		assert.Equal(t, []any{"eth-client-docker-image-builder"}, fake.tokenBodies[0]["repositories"])
		assert.Equal(t, map[string]any{"actions": "write"}, fake.tokenBodies[0]["permissions"])
	})
}

func TestGitHubAppTokenSourceCachesAndRefreshesBeforeExpiry(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	fake := &fakeGitHub{tokenExpires: start.Add(time.Hour)}
	srv := httptest.NewServer(fake.handler(t))
	t.Cleanup(srv.Close)

	now := start
	ts := newTestTokenSource(t, newTestAppKey(t), srv.URL)
	ts.now = func() time.Time { return now }

	first, err := ts.Token(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "ghs_installation_1", first)

	// Still more than the refresh margin away from expiry: cached.
	now = start.Add(54 * time.Minute)
	cached, err := ts.Token(context.Background())
	require.NoError(t, err)
	assert.Equal(t, first, cached)
	fake.locked(func() { assert.Equal(t, 1, fake.tokenCalls) })

	// Within the refresh margin: a new token is minted.
	fake.locked(func() { fake.tokenExpires = start.Add(2 * time.Hour) })
	now = start.Add(56 * time.Minute)
	refreshed, err := ts.Token(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "ghs_installation_2", refreshed)
	fake.locked(func() { assert.Equal(t, 2, fake.tokenCalls) })
}

func TestGitHubAppTokenSourceReportsExchangeFailure(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	_, err := newTestTokenSource(t, newTestAppKey(t), srv.URL).Token(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
}

func newFakeBackedGitHubHandler(t *testing.T, cfg GitHubConfig, fake *fakeGitHub) *GitHubHandler {
	t.Helper()

	srv := httptest.NewServer(fake.handler(t))
	t.Cleanup(srv.Close)

	log := logrus.New()
	log.SetOutput(io.Discard)

	h := NewGitHubHandler(log, cfg, nil)
	h.apiBase = srv.URL

	if h.appTokens != nil {
		h.appTokens.apiBase = srv.URL
	}

	return h
}

func TestGitHubHandlerUsesAppInstallationToken(t *testing.T) {
	t.Parallel()

	fake := &fakeGitHub{tokenExpires: time.Now().Add(time.Hour)}
	h := newFakeBackedGitHubHandler(t, GitHubConfig{
		App: &GitHubAppConfig{AppID: testAppID, InstallationID: testInstallationID, PrivateKey: newTestAppKey(t)},
	}, fake)

	rec, req := triggerRequest(t, `{"repository":"`+defaultAllowedRepository+`","workflow":"build.yml"}`)
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"run_id":42`)

	// Dispatch and run lookup share one cached installation token.
	fake.locked(func() {
		assert.Equal(t, 1, fake.tokenCalls)
		assert.Equal(t, []string{"Bearer ghs_installation_1", "Bearer ghs_installation_1"}, fake.apiAuth)
		assert.Equal(t, []any{"eth-client-docker-image-builder"}, fake.tokenBodies[0]["repositories"])
		assert.Equal(t, map[string]any{"actions": "write"}, fake.tokenBodies[0]["permissions"])
	})
}

func TestGitHubHandlerTokenPathUnchanged(t *testing.T) {
	t.Parallel()

	for name, cfg := range map[string]GitHubConfig{
		"token only": {Token: "test-token"},
		"token wins over app": {
			Token: "test-token",
			App:   &GitHubAppConfig{AppID: testAppID, InstallationID: testInstallationID, PrivateKey: newTestAppKey(t)},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fake := &fakeGitHub{tokenExpires: time.Now().Add(time.Hour)}
			h := newFakeBackedGitHubHandler(t, cfg, fake)

			rec, req := triggerRequest(t, `{"repository":"`+defaultAllowedRepository+`","workflow":"build.yml"}`)
			h.ServeHTTP(rec, req)

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			fake.locked(func() {
				assert.Equal(t, 0, fake.tokenCalls)
				assert.Equal(t, []string{"Bearer test-token", "Bearer test-token"}, fake.apiAuth)
			})
		})
	}
}

func TestGitHubHandlerAppTokenFailureIsBadGateway(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	log := logrus.New()
	log.SetOutput(io.Discard)

	h := NewGitHubHandler(log, GitHubConfig{
		App: &GitHubAppConfig{AppID: testAppID, InstallationID: testInstallationID, PrivateKey: newTestAppKey(t)},
	}, nil)
	h.apiBase = srv.URL
	h.appTokens.apiBase = srv.URL

	rec, req := triggerRequest(t, `{"repository":"`+defaultAllowedRepository+`","workflow":"build.yml"}`)
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Contains(t, rec.Body.String(), "github authentication failed")
}
