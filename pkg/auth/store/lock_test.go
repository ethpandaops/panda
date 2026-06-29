//go:build unix && !aix

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	authclient "github.com/ethpandaops/panda/pkg/auth/client"
)

func TestTryFileLockIsMutuallyExclusive(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "creds.json")

	release1, ok1, err1 := tryFileLock(path)
	if err1 != nil || !ok1 {
		t.Fatalf("first lock should be acquired: ok=%v err=%v", ok1, err1)
	}

	if _, ok2, _ := tryFileLock(path); ok2 {
		t.Fatal("second lock should fail while the first is held")
	}

	release1()

	release3, ok3, err3 := tryFileLock(path)
	if err3 != nil || !ok3 {
		t.Fatalf("lock should be acquirable after release: ok=%v err=%v", ok3, err3)
	}
	release3()
}

// TestSaveAndClearFailClosedWhileLockHeld verifies that an interactive write
// refuses to proceed (rather than clobber a rotation) while another process
// holds the credentials lock, and succeeds once it is released.
func TestSaveAndClearFailClosedWhileLockHeld(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "creds.json")
	st := New(logrus.New(), Config{
		Path:          path,
		WriteLockWait: 100 * time.Millisecond,
	}).(*store)

	// Simulate a refresh in another process holding the lock.
	release, ok, err := tryFileLock(path)
	if err != nil || !ok {
		t.Fatalf("failed to take lock: ok=%v err=%v", ok, err)
	}

	tokens := &authclient.Tokens{
		AccessToken:  "access",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(time.Hour),
	}

	if err := st.Save(tokens); !errors.Is(err, ErrCredentialBusy) {
		t.Fatalf("expected ErrCredentialBusy while lock held, got %v", err)
	}

	if err := st.Clear(); !errors.Is(err, ErrCredentialBusy) {
		t.Fatalf("expected ErrCredentialBusy from Clear while lock held, got %v", err)
	}

	release()

	if err := st.Save(tokens); err != nil {
		t.Fatalf("Save should succeed after the lock is released: %v", err)
	}
}

// TestRefreshReusesTokenRotatedByAnotherProcess verifies the reload-recheck
// step: if another process rotated the on-disk token after we decided to
// refresh but before we acquired the lock, we reuse that token instead of
// refreshing again (which would present a token the other process revoked).
func TestRefreshReusesTokenRotatedByAnotherProcess(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "creds.json")
	client := &countingAuthClient{}
	store := New(logrus.New(), Config{
		Path:          path,
		AuthClient:    client,
		RefreshBuffer: 5 * time.Minute,
	}).(*store)

	// In-memory token is due for refresh (inside the buffer).
	store.tokens = &authclient.Tokens{
		AccessToken:  "stale",
		RefreshToken: "stale-rt",
		ExpiresAt:    time.Now().Add(time.Minute),
	}

	// Another process has already written a fresh token to the shared file.
	writeTokens(t, path, &authclient.Tokens{
		AccessToken:  "fresh",
		RefreshToken: "fresh-rt",
		ExpiresIn:    3600,
		ExpiresAt:    time.Now().Add(time.Hour),
	})

	token, err := store.GetAccessToken()
	if err != nil {
		t.Fatalf("GetAccessToken returned error: %v", err)
	}

	if token != "fresh" {
		t.Fatalf("expected the token written by another process, got %q", token)
	}

	if client.refreshCalls.Load() != 0 {
		t.Fatalf("expected no provider refresh, got %d", client.refreshCalls.Load())
	}
}

// TestSharedCredentialRefreshNeverReusesRevokedToken hammers several stores
// that share one credentials file against a provider that rotates and revokes
// like authentik. The file lock must ensure no store ever presents a
// rotated-away token (which would return invalid_grant).
func TestSharedCredentialRefreshNeverReusesRevokedToken(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "creds.json")
	client := newRotatingAuthClient("rt-0")
	writeTokens(t, path, &authclient.Tokens{
		AccessToken:          "at-0",
		RefreshToken:         "rt-0",
		ExpiresIn:            3600,
		ExpiresAt:            time.Now().Add(time.Hour),
		RefreshTokenIssuedAt: time.Now(),
	})

	const (
		stores     = 4
		iterations = 50
	)

	newSharedStore := func() *store {
		return New(logrus.New(), Config{
			Path:            path,
			AuthClient:      client,
			RefreshBuffer:   5 * time.Minute,
			RefreshTokenTTL: time.Nanosecond, // always past half-life: forces a refresh every call
		}).(*store)
	}

	start := make(chan struct{})

	var wg sync.WaitGroup

	wg.Add(stores)

	for range stores {
		s := newSharedStore()

		go func() {
			defer wg.Done()

			<-start // release all goroutines together to maximise contention

			for range iterations {
				token, err := s.GetAccessToken()
				if err != nil {
					t.Errorf("GetAccessToken returned error: %v", err)

					return
				}

				if token == "" {
					t.Error("GetAccessToken returned an empty token")

					return
				}
			}
		}()
	}

	close(start)
	wg.Wait()

	if got := client.invalidGrants.Load(); got != 0 {
		t.Fatalf("expected zero invalid_grant (revoked-token reuse), got %d", got)
	}

	if got := client.refreshCalls.Load(); got == 0 {
		t.Fatal("expected at least one provider refresh")
	}

	// The shared credential must end in a usable, authenticated state.
	final := New(logrus.New(), Config{Path: path, AuthClient: client}).(*store)
	if !final.IsAuthenticated() {
		t.Fatal("shared credential is not authenticated after concurrent refreshes")
	}
}

func writeTokens(t *testing.T, path string, tokens *authclient.Tokens) {
	t.Helper()

	data, err := json.MarshalIndent(tokens, "", "  ")
	if err != nil {
		t.Fatalf("marshaling tokens: %v", err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("writing tokens: %v", err)
	}
}

// rotatingAuthClient models authentik: each refresh issues a new refresh token
// and revokes the presented one. Presenting a revoked token returns
// invalid_grant.
type rotatingAuthClient struct {
	mu            sync.Mutex
	counter       int
	valid         map[string]struct{}
	refreshCalls  atomic.Int64
	invalidGrants atomic.Int64
}

func newRotatingAuthClient(initial string) *rotatingAuthClient {
	return &rotatingAuthClient{valid: map[string]struct{}{initial: {}}}
}

func (c *rotatingAuthClient) BeginDeviceLogin(_ context.Context) (*authclient.DeviceAuth, error) {
	return nil, errors.New("not implemented")
}

func (c *rotatingAuthClient) PollDeviceLogin(_ context.Context, _ *authclient.DeviceAuth) (*authclient.Tokens, error) {
	return nil, errors.New("not implemented")
}

func (c *rotatingAuthClient) ClientCredentials(_ context.Context) (*authclient.Tokens, error) {
	return nil, errors.New("not implemented")
}

func (c *rotatingAuthClient) Refresh(_ context.Context, refreshToken string) (*authclient.Tokens, error) {
	c.refreshCalls.Add(1)

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.valid[refreshToken]; !ok {
		c.invalidGrants.Add(1)

		return nil, fmt.Errorf("token endpoint returned status 400: {\"error\": \"invalid_grant\"}")
	}

	delete(c.valid, refreshToken)
	c.counter++
	next := fmt.Sprintf("rt-%d", c.counter)
	c.valid[next] = struct{}{}

	return &authclient.Tokens{
		AccessToken:          fmt.Sprintf("at-%d", c.counter),
		RefreshToken:         next,
		TokenType:            "Bearer",
		ExpiresIn:            3600,
		ExpiresAt:            time.Now().Add(time.Hour),
		RefreshTokenIssuedAt: time.Now(),
	}, nil
}
