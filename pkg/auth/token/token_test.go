package token

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/auth/client"
)

func discardLog() logrus.FieldLogger {
	log := logrus.New()
	log.SetOutput(discardWriter{})

	return log
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// fakeAuthClient is a client.Client whose ClientCredentials mints
// "svc-token-<n>" with the configured lifetime and can be made to fail.
type fakeAuthClient struct {
	mu        sync.Mutex
	mints     int
	expiresIn int
	fail      bool
}

func (f *fakeAuthClient) BeginDeviceLogin(_ context.Context) (*client.DeviceAuth, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeAuthClient) PollDeviceLogin(_ context.Context, _ *client.DeviceAuth) (*client.Tokens, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeAuthClient) Refresh(_ context.Context, _ string) (*client.Tokens, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeAuthClient) ClientCredentials(_ context.Context) (*client.Tokens, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.mints++
	if f.fail {
		return nil, errors.New("mint failed")
	}

	return &client.Tokens{
		AccessToken: fmt.Sprintf("svc-token-%d", f.mints),
		TokenType:   "Bearer",
		ExpiresIn:   f.expiresIn,
		ExpiresAt:   time.Now().Add(time.Duration(f.expiresIn) * time.Second),
	}, nil
}

func (f *fakeAuthClient) mintCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.mints
}

func TestClientCredentialsSourceCachesUntilRefreshFraction(t *testing.T) {
	t.Parallel()

	auth := &fakeAuthClient{expiresIn: 3600}
	src := NewClientCredentialsSource(discardLog(), auth, time.Second)

	first, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("first Token error: %v", err)
	}

	second, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("second Token error: %v", err)
	}

	if first != "svc-token-1" || second != "svc-token-1" {
		t.Fatalf("expected cached svc-token-1, got %q then %q", first, second)
	}

	if got := auth.mintCount(); got != 1 {
		t.Fatalf("expected 1 mint for a cached token, got %d", got)
	}
}

func TestClientCredentialsSourceReMintsWithinBuffer(t *testing.T) {
	t.Parallel()

	// A 1s lifetime is inside the refresh buffer, so each call re-mints.
	auth := &fakeAuthClient{expiresIn: 1}
	src := NewClientCredentialsSource(discardLog(), auth, time.Second)

	first, _ := src.Token(context.Background())
	second, _ := src.Token(context.Background())

	if first == second {
		t.Fatalf("expected a re-mint, got %q twice", first)
	}

	if got := auth.mintCount(); got != 2 {
		t.Fatalf("expected 2 mints, got %d", got)
	}
}

func TestClientCredentialsSourceInvalidateForcesReMint(t *testing.T) {
	t.Parallel()

	auth := &fakeAuthClient{expiresIn: 3600}
	src := NewClientCredentialsSource(discardLog(), auth, time.Second)

	first, _ := src.Token(context.Background())

	src.Invalidate()

	second, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("Token after Invalidate error: %v", err)
	}

	if first == second {
		t.Fatalf("expected a fresh token after Invalidate, got %q twice", first)
	}

	if got := auth.mintCount(); got != 2 {
		t.Fatalf("expected 2 mints after Invalidate, got %d", got)
	}
}

func TestClientCredentialsSourceServesCachedTokenAcrossMintOutage(t *testing.T) {
	t.Parallel()

	// 60s lifetime sits inside the 5m buffer, so the next call attempts a
	// re-mint — but the cached token is still valid.
	auth := &fakeAuthClient{expiresIn: 60}
	src := NewClientCredentialsSource(discardLog(), auth, time.Second)

	first, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("first Token error: %v", err)
	}

	auth.mu.Lock()
	auth.fail = true
	auth.mu.Unlock()

	got, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("expected cached token across mint outage, got error: %v", err)
	}

	if got != first {
		t.Fatalf("expected cached token %q across outage, got %q", first, got)
	}
}

// fakeStore is a minimal store.Store for exercising the refresh source.
type fakeStore struct {
	token       string
	invalidated bool
}

func (f *fakeStore) Path() string              { return "" }
func (f *fakeStore) Save(*client.Tokens) error { return nil }

func (f *fakeStore) Load() (*client.Tokens, error) {
	if f.token == "" {
		return nil, nil
	}

	return &client.Tokens{AccessToken: f.token}, nil
}

func (f *fakeStore) Clear() error                    { return nil }
func (f *fakeStore) GetAccessToken() (string, error) { return f.token, nil }
func (f *fakeStore) Invalidate()                     { f.invalidated = true }
func (f *fakeStore) IsAuthenticated() bool           { return f.token != "" }

func TestRefreshSourceDelegatesToStore(t *testing.T) {
	t.Parallel()

	fs := &fakeStore{token: "access-123"}
	src := NewRefreshSource(fs)

	tok, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("Token error: %v", err)
	}

	if tok != "access-123" {
		t.Fatalf("Token = %q, want access-123", tok)
	}

	src.Invalidate()
	if !fs.invalidated {
		t.Fatal("Invalidate did not propagate to the store")
	}
}

func TestRefreshSourceDetectsLogoutOnReload(t *testing.T) {
	t.Parallel()

	fs := &fakeStore{token: "access-123"}
	src := NewRefreshSource(fs)

	if _, err := src.Token(context.Background()); err != nil {
		t.Fatalf("Token error while authenticated: %v", err)
	}

	// Simulate a logout (credentials file cleared) under a running server.
	fs.token = ""

	_, err := src.Token(context.Background())
	if !errors.Is(err, ErrNotAuthenticated) {
		t.Fatalf("expected ErrNotAuthenticated after logout, got %v", err)
	}
}

func TestNewSourceNilWhenUnconfigured(t *testing.T) {
	t.Parallel()

	if src := NewSource(discardLog(), Config{}); src != nil {
		t.Fatal("NewSource with no issuer/clientID should return nil")
	}

	if src := NewSource(discardLog(), Config{IssuerURL: "https://issuer.example"}); src != nil {
		t.Fatal("NewSource with no clientID should return nil")
	}
}

func TestNewSourceBuildsConfiguredGrants(t *testing.T) {
	t.Parallel()

	cc := NewSource(discardLog(), Config{
		IssuerURL: "https://issuer.example",
		ClientID:  "panda-proxy",
		Mode:      ModeClientCredentials,
	})
	if cc == nil {
		t.Fatal("NewSource for client_credentials returned nil")
	}

	interactive := NewSource(discardLog(), Config{
		IssuerURL: "https://issuer.example",
		ClientID:  "panda",
	})
	if interactive == nil {
		t.Fatal("NewSource for the interactive grant returned nil")
	}
}
