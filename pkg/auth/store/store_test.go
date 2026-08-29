package store

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	authclient "github.com/ethpandaops/panda/pkg/auth/client"
)

func TestSaveRefusesCredentialDowngrade(t *testing.T) {
	t.Parallel()

	st := New(logrus.New(), Config{
		Path: filepath.Join(t.TempDir(), "creds.json"),
	}).(*store)

	// Seed a refreshable credential.
	if err := st.Save(&authclient.Tokens{
		AccessToken:  "access",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("seeding refreshable credential: %v", err)
	}

	// A login that returns no refresh token must be refused, not silently saved.
	err := st.Save(&authclient.Tokens{
		AccessToken: "access-2",
		ExpiresAt:   time.Now().Add(time.Hour),
	})
	if !errors.Is(err, ErrCredentialDowngrade) {
		t.Fatalf("expected ErrCredentialDowngrade, got %v", err)
	}

	// The refreshable credential must be left intact.
	got, err := st.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got == nil || got.RefreshToken != "refresh" {
		t.Fatalf("refreshable credential was overwritten: %+v", got)
	}

	// A refreshable login is allowed; after logout a tokenless login is allowed
	// because there is no refreshable credential to protect.
	if err := st.Save(&authclient.Tokens{
		AccessToken: "a", RefreshToken: "r2", ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("refreshable save should succeed: %v", err)
	}

	if err := st.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}

	if err := st.Save(&authclient.Tokens{
		AccessToken: "a", ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("tokenless save with no existing credential should succeed: %v", err)
	}
}

func TestGetAccessTokenKeepsValidTokenWithoutRefreshToken(t *testing.T) {
	t.Parallel()

	client := &stubAuthClient{}
	store := New(logrus.New(), Config{
		Path:          filepath.Join(t.TempDir(), "creds.json"),
		AuthClient:    client,
		RefreshBuffer: 5 * time.Minute,
	}).(*store)
	store.tokens = &authclient.Tokens{
		AccessToken: "still-valid",
		ExpiresAt:   time.Now().Add(2 * time.Minute),
	}

	token, err := store.GetAccessToken()
	if err != nil {
		t.Fatalf("GetAccessToken returned error: %v", err)
	}

	if token != "still-valid" {
		t.Fatalf("unexpected token: %q", token)
	}

	if client.refreshCalls != 0 {
		t.Fatalf("expected no refresh attempts, got %d", client.refreshCalls)
	}
}

func TestGetAccessTokenFallsBackWhenRefreshFailsButTokenIsStillValid(t *testing.T) {
	t.Parallel()

	client := &stubAuthClient{refreshErr: errors.New("temporary failure")}
	store := New(logrus.New(), Config{
		Path:          filepath.Join(t.TempDir(), "creds.json"),
		AuthClient:    client,
		RefreshBuffer: 5 * time.Minute,
	}).(*store)
	store.tokens = &authclient.Tokens{
		AccessToken:  "still-valid",
		RefreshToken: "refresh-token",
		ExpiresAt:    time.Now().Add(2 * time.Minute),
	}

	token, err := store.GetAccessToken()
	if err != nil {
		t.Fatalf("GetAccessToken returned error: %v", err)
	}

	if token != "still-valid" {
		t.Fatalf("unexpected token: %q", token)
	}

	if client.refreshCalls != 1 {
		t.Fatalf("expected 1 refresh attempt, got %d", client.refreshCalls)
	}
}

func TestGetAccessTokenRefreshesAtRefreshTokenHalfLife(t *testing.T) {
	t.Parallel()

	client := &stubAuthClient{}
	store := New(logrus.New(), Config{
		Path:            filepath.Join(t.TempDir(), "creds.json"),
		AuthClient:      client,
		RefreshBuffer:   5 * time.Minute,
		RefreshTokenTTL: 30 * 24 * time.Hour, // 30 days
	}).(*store)
	store.tokens = &authclient.Tokens{
		AccessToken:          "still-valid",
		RefreshToken:         "refresh-token",
		ExpiresAt:            time.Now().Add(time.Hour),            // access token is fresh
		RefreshTokenIssuedAt: time.Now().Add(-16 * 24 * time.Hour), // issued 16 days ago (past 50%)
	}

	token, err := store.GetAccessToken()
	if err != nil {
		t.Fatalf("GetAccessToken returned error: %v", err)
	}

	if token != "refreshed-token" {
		t.Fatalf("expected refreshed token, got %q", token)
	}

	if client.refreshCalls != 1 {
		t.Fatalf("expected 1 refresh call, got %d", client.refreshCalls)
	}
}

func TestGetAccessTokenDoesNotRefreshBeforeRefreshTokenHalfLife(t *testing.T) {
	t.Parallel()

	client := &stubAuthClient{}
	store := New(logrus.New(), Config{
		Path:            filepath.Join(t.TempDir(), "creds.json"),
		AuthClient:      client,
		RefreshBuffer:   5 * time.Minute,
		RefreshTokenTTL: 30 * 24 * time.Hour, // 30 days
	}).(*store)
	store.tokens = &authclient.Tokens{
		AccessToken:          "still-valid",
		RefreshToken:         "refresh-token",
		ExpiresAt:            time.Now().Add(time.Hour),            // access token is fresh
		RefreshTokenIssuedAt: time.Now().Add(-10 * 24 * time.Hour), // issued 10 days ago (before 50%)
	}

	token, err := store.GetAccessToken()
	if err != nil {
		t.Fatalf("GetAccessToken returned error: %v", err)
	}

	if token != "still-valid" {
		t.Fatalf("expected original token, got %q", token)
	}

	if client.refreshCalls != 0 {
		t.Fatalf("expected no refresh calls, got %d", client.refreshCalls)
	}
}

func TestGetAccessTokenSerializesConcurrentRefreshes(t *testing.T) {
	t.Parallel()

	client := &countingAuthClient{}
	store := New(logrus.New(), Config{
		Path:          filepath.Join(t.TempDir(), "creds.json"),
		AuthClient:    client,
		RefreshBuffer: 5 * time.Minute,
	}).(*store)
	store.tokens = &authclient.Tokens{
		AccessToken:  "expiring",
		RefreshToken: "refresh-token",
		ExpiresAt:    time.Now().Add(time.Minute), // within the refresh buffer
	}

	const goroutines = 16

	var wg sync.WaitGroup

	wg.Add(goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()

			if _, err := store.GetAccessToken(); err != nil {
				t.Errorf("GetAccessToken returned error: %v", err)
			}
		}()
	}

	wg.Wait()

	if got := client.refreshCalls.Load(); got != 1 {
		t.Fatalf("expected exactly 1 provider refresh call, got %d", got)
	}
}

func TestGetAccessTokenRefreshesAtAccessTokenHalfLife(t *testing.T) {
	t.Parallel()

	client := &stubAuthClient{}
	store := New(logrus.New(), Config{
		Path:          filepath.Join(t.TempDir(), "creds.json"),
		AuthClient:    client,
		RefreshBuffer: 5 * time.Minute,
	}).(*store)
	// 1h token with 20m left = 66% elapsed: past the 50% refresh point, but
	// well outside the 5m expiry buffer.
	store.tokens = &authclient.Tokens{
		AccessToken:  "aging",
		RefreshToken: "refresh-token",
		ExpiresIn:    3600,
		ExpiresAt:    time.Now().Add(20 * time.Minute),
	}

	token, err := store.GetAccessToken()
	if err != nil {
		t.Fatalf("GetAccessToken returned error: %v", err)
	}

	if token != "refreshed-token" {
		t.Fatalf("expected proactive refresh past 50%% of lifetime, got %q", token)
	}

	if client.refreshCalls != 1 {
		t.Fatalf("expected 1 refresh call, got %d", client.refreshCalls)
	}
}

func TestGetAccessTokenDoesNotRefreshBeforeAccessTokenHalfLife(t *testing.T) {
	t.Parallel()

	client := &stubAuthClient{}
	store := New(logrus.New(), Config{
		Path:          filepath.Join(t.TempDir(), "creds.json"),
		AuthClient:    client,
		RefreshBuffer: 5 * time.Minute,
	}).(*store)
	// 1h token with 40m left = 33% elapsed: before the 50% refresh point.
	store.tokens = &authclient.Tokens{
		AccessToken:  "fresh",
		RefreshToken: "refresh-token",
		ExpiresIn:    3600,
		ExpiresAt:    time.Now().Add(40 * time.Minute),
	}

	token, err := store.GetAccessToken()
	if err != nil {
		t.Fatalf("GetAccessToken returned error: %v", err)
	}

	if token != "fresh" {
		t.Fatalf("expected original token before 50%% of lifetime, got %q", token)
	}

	if client.refreshCalls != 0 {
		t.Fatalf("expected no refresh calls, got %d", client.refreshCalls)
	}
}

func TestInvalidateForcesRefreshThenClears(t *testing.T) {
	t.Parallel()

	client := &stubAuthClient{}
	store := New(logrus.New(), Config{
		Path:          filepath.Join(t.TempDir(), "creds.json"),
		AuthClient:    client,
		RefreshBuffer: 5 * time.Minute,
	}).(*store)
	// A fresh token that would not otherwise refresh (0% elapsed).
	store.tokens = &authclient.Tokens{
		AccessToken:  "fresh",
		RefreshToken: "refresh-token",
		ExpiresIn:    3600,
		ExpiresAt:    time.Now().Add(time.Hour),
	}

	store.Invalidate()

	token, err := store.GetAccessToken()
	if err != nil {
		t.Fatalf("GetAccessToken returned error: %v", err)
	}

	if token != "refreshed-token" {
		t.Fatalf("expected forced refresh after Invalidate, got %q", token)
	}

	if client.refreshCalls != 1 {
		t.Fatalf("expected 1 refresh call after Invalidate, got %d", client.refreshCalls)
	}

	// The forced-refresh flag must clear: the now-fresh token should not
	// refresh again on the next call.
	if _, err := store.GetAccessToken(); err != nil {
		t.Fatalf("second GetAccessToken returned error: %v", err)
	}

	if client.refreshCalls != 1 {
		t.Fatalf("expected no extra refresh after the flag cleared, got %d", client.refreshCalls)
	}
}

func TestGetAccessTokenStopsRefreshingAfterInvalidGrant(t *testing.T) {
	t.Parallel()

	client := &stubAuthClient{refreshErr: errors.New(
		`token endpoint returned status 400: {"error": "invalid_grant"}`,
	)}
	store := New(logrus.New(), Config{
		Path:          filepath.Join(t.TempDir(), "creds.json"),
		AuthClient:    client,
		RefreshBuffer: 5 * time.Minute,
	}).(*store)
	store.tokens = &authclient.Tokens{
		AccessToken:  "expired",
		RefreshToken: "burned-token",
		ExpiresAt:    time.Now().Add(-time.Minute),
	}

	if _, err := store.GetAccessToken(); !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("expected ErrReauthRequired, got %v", err)
	}

	if client.refreshCalls != 1 {
		t.Fatalf("expected 1 refresh attempt, got %d", client.refreshCalls)
	}

	// Retrying with the same rejected token must not hit the provider again.
	if _, err := store.GetAccessToken(); !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("expected ErrReauthRequired on retry, got %v", err)
	}

	if client.refreshCalls != 1 {
		t.Fatalf("expected no further refresh attempts, got %d", client.refreshCalls)
	}
}

func TestGetAccessTokenRecoversWhenNewCredentialWritten(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "creds.json")

	client := &stubAuthClient{refreshErr: errors.New(
		`token endpoint returned status 400: {"error": "invalid_grant"}`,
	)}
	store := New(logrus.New(), Config{
		Path:          path,
		AuthClient:    client,
		RefreshBuffer: 5 * time.Minute,
	}).(*store)
	store.tokens = &authclient.Tokens{
		AccessToken:  "expired",
		RefreshToken: "burned-token",
		ExpiresAt:    time.Now().Add(-time.Minute),
	}

	if _, err := store.GetAccessToken(); !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("expected ErrReauthRequired, got %v", err)
	}

	// Another process (e.g. the host CLI running `panda auth login`) writes a
	// fresh credential to the shared file.
	other := New(logrus.New(), Config{Path: path})
	if err := other.Save(&authclient.Tokens{
		AccessToken:  "expired-too",
		RefreshToken: "fresh-token",
		ExpiresAt:    time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("saving replacement credential: %v", err)
	}

	client.refreshErr = nil

	token, err := store.GetAccessToken()
	if err != nil {
		t.Fatalf("GetAccessToken after new credential: %v", err)
	}

	if token != "refreshed-token" {
		t.Fatalf("expected refreshed token, got %q", token)
	}

	if client.refreshCalls != 2 {
		t.Fatalf("expected refresh to resume with the new token, got %d calls", client.refreshCalls)
	}
}

type stubAuthClient struct {
	refreshCalls int
	refreshErr   error
}

func (s *stubAuthClient) BeginDeviceLogin(_ context.Context) (*authclient.DeviceAuth, error) {
	return nil, errors.New("not implemented")
}

func (s *stubAuthClient) PollDeviceLogin(_ context.Context, _ *authclient.DeviceAuth) (*authclient.Tokens, error) {
	return nil, errors.New("not implemented")
}

func (s *stubAuthClient) ClientCredentials(_ context.Context) (*authclient.Tokens, error) {
	return nil, errors.New("not implemented")
}

func (s *stubAuthClient) Refresh(_ context.Context, _ string) (*authclient.Tokens, error) {
	s.refreshCalls++
	if s.refreshErr != nil {
		return nil, s.refreshErr
	}

	return &authclient.Tokens{
		AccessToken:  "refreshed-token",
		RefreshToken: "refresh-token",
		ExpiresIn:    3600,
		ExpiresAt:    time.Now().Add(time.Hour),
		TokenType:    "Bearer",
	}, nil
}

type countingAuthClient struct {
	refreshCalls atomic.Int64
}

func (s *countingAuthClient) BeginDeviceLogin(_ context.Context) (*authclient.DeviceAuth, error) {
	return nil, errors.New("not implemented")
}

func (s *countingAuthClient) PollDeviceLogin(_ context.Context, _ *authclient.DeviceAuth) (*authclient.Tokens, error) {
	return nil, errors.New("not implemented")
}

func (s *countingAuthClient) ClientCredentials(_ context.Context) (*authclient.Tokens, error) {
	return nil, errors.New("not implemented")
}

func (s *countingAuthClient) Refresh(_ context.Context, _ string) (*authclient.Tokens, error) {
	s.refreshCalls.Add(1)

	return &authclient.Tokens{
		AccessToken:  "refreshed-token",
		RefreshToken: "refresh-token",
		ExpiresIn:    3600,
		ExpiresAt:    time.Now().Add(time.Hour),
		TokenType:    "Bearer",
	}, nil
}
