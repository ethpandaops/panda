package store

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	authclient "github.com/ethpandaops/panda/pkg/auth/client"
)

func TestGetAccessTokenKeepsValidTokenWithoutRefreshToken(t *testing.T) {
	t.Parallel()

	client := &stubAuthClient{}
	store := New(logrus.New(), Config{
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

type stubAuthClient struct {
	refreshCalls int
	refreshErr   error
}

func (s *stubAuthClient) Login(_ context.Context) (*authclient.Tokens, error) {
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

func (s *countingAuthClient) Login(_ context.Context) (*authclient.Tokens, error) {
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
