package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/ethpandaops/panda/pkg/auth/github"
)

// barrierGitHubClient blocks inside GetUser until told to proceed, so a test
// can force two concurrent refresh requests to both pass the initial session
// lookup before either one rotates the token.
type barrierGitHubClient struct {
	user    *github.GitHubUser
	arrived chan struct{}
	release chan struct{}
}

func (b *barrierGitHubClient) GetAuthorizationURL(_, _, _ string) string {
	return "https://github.example.test/oauth"
}

func (b *barrierGitHubClient) ExchangeCode(_ context.Context, _, _ string) (*github.TokenResponse, error) {
	return &github.TokenResponse{AccessToken: "github-access-token"}, nil
}

func (b *barrierGitHubClient) GetUser(_ context.Context, _ string) (*github.GitHubUser, error) {
	b.arrived <- struct{}{}
	<-b.release

	return b.user, nil
}

func postRefreshToken(svc *authorizationServer, refreshToken string) (int, tokenResponseBody) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://internal-proxy/auth/token", strings.NewReader(url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {"panda"},
		"resource":      {testIssuerURL},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	svc.handleToken(rec, req)

	var body tokenResponseBody
	_ = json.Unmarshal(rec.Body.Bytes(), &body)

	return rec.Code, body
}

// TestConcurrentRefreshOfSameTokenRotatesExactlyOnce presents the same refresh
// token from two concurrent requests. Only one may succeed in rotating it;
// the other must see the token as already consumed rather than mint its own
// independent successor.
func TestConcurrentRefreshOfSameTokenRotatesExactlyOnce(t *testing.T) {
	t.Parallel()

	svc := newTestAuthorizationServer(t, []string{"ethpandaops"})
	barrier := &barrierGitHubClient{
		user: &github.GitHubUser{ID: 42, Login: "sam", Organizations: []string{"ethpandaops"}},
		// Buffered so both goroutines can signal arrival without blocking on a
		// reader that hasn't reached the receive yet.
		arrived: make(chan struct{}, 2),
		release: make(chan struct{}),
	}
	svc.github = barrier

	refreshToken, err := svc.issueRefreshToken("panda", testIssuerURL, "sam", 42, "github-access-token", []string{"ethpandaops"})
	if err != nil {
		t.Fatalf("issueRefreshToken failed: %v", err)
	}

	type result struct {
		status int
		body   tokenResponseBody
	}

	results := make(chan result, 2)
	var wg sync.WaitGroup
	wg.Add(2)

	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			status, body := postRefreshToken(svc, refreshToken)
			results <- result{status, body}
		}()
	}

	// Wait for both requests to reach the GitHub lookup, then release them so
	// they attempt to rotate at the same time.
	<-barrier.arrived
	<-barrier.arrived
	close(barrier.release)

	wg.Wait()
	close(results)

	var successes, rejections int
	var newTokens []string

	for r := range results {
		switch r.status {
		case http.StatusOK:
			successes++
			if r.body.RefreshToken == "" {
				t.Fatal("successful refresh did not return a new refresh token")
			}
			newTokens = append(newTokens, r.body.RefreshToken)
		case http.StatusBadRequest:
			rejections++
		default:
			t.Fatalf("unexpected status %d", r.status)
		}
	}

	if successes != 1 || rejections != 1 {
		t.Fatalf("expected exactly one success and one rejection, got %d successes and %d rejections", successes, rejections)
	}

	svc.refreshSessionsMu.RLock()
	_, originalStillLive := svc.refreshSessions[refreshToken]
	_, newTokenLive := svc.refreshSessions[newTokens[0]]
	total := len(svc.refreshSessions)
	svc.refreshSessionsMu.RUnlock()

	if originalStillLive {
		t.Fatal("original refresh token should have been consumed")
	}
	if !newTokenLive {
		t.Fatal("the successful rotation's new refresh token should be live")
	}
	if total != 1 {
		t.Fatalf("expected exactly one live refresh session after rotation, got %d", total)
	}
}
