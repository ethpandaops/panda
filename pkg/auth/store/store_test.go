package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	authclient "github.com/ethpandaops/mcp/pkg/auth/client"
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

type stubAuthClient struct {
	refreshCalls int
	refreshErr   error
}

func (s *stubAuthClient) Login(_ context.Context) (*authclient.Tokens, error) {
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
