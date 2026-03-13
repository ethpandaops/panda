package tokenstore

import (
	"testing"
	"time"
)

func TestStoreRegisterValidateRevokeAndStop(t *testing.T) {
	t.Parallel()

	store := New(time.Minute)
	t.Cleanup(store.Stop)

	token := store.Register("value-1")
	if token == "" {
		t.Fatal("Register() returned empty token")
	}

	if got := store.Validate(token); got != "value-1" {
		t.Fatalf("Validate() = %q, want value-1", got)
	}

	store.Revoke("value-1")
	if got := store.Validate(token); got != "" {
		t.Fatalf("Validate() after revoke = %q, want empty string", got)
	}

	store.Stop()
	store.Stop()
}

func TestStoreValidateCleanupAndGenerateToken(t *testing.T) {
	t.Parallel()

	store := &Store{
		tokens: map[string]entry{
			"expired": {value: "old", expiresAt: time.Now().Add(-time.Minute)},
			"valid":   {value: "new", expiresAt: time.Now().Add(time.Minute)},
		},
		ttl:    time.Minute,
		stopCh: make(chan struct{}),
	}

	if got := store.Validate("expired"); got != "" {
		t.Fatalf("Validate(expired) = %q, want empty string", got)
	}

	store.cleanup()
	if _, ok := store.tokens["expired"]; ok {
		t.Fatalf("cleanup() kept expired token: %#v", store.tokens)
	}
	if got := store.tokens["valid"].value; got != "new" {
		t.Fatalf("cleanup() valid token = %#v, want preserved valid entry", store.tokens["valid"])
	}

	generated := generateToken()
	if generated == "" {
		t.Fatal("generateToken() returned empty token")
	}
	if other := generateToken(); other == generated {
		t.Fatal("generateToken() returned duplicate tokens")
	}
}
