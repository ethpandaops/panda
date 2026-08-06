package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFaucetSessionRateLimiterAllow(t *testing.T) {
	l := newFaucetSessionRateLimiter(2, time.Hour)
	base := time.Unix(1_700_000_000, 0)

	for i := 0; i < 2; i++ {
		if !l.allow("u1", base) {
			t.Fatalf("start %d within the limit should be allowed", i+1)
		}
	}

	if l.allow("u1", base) {
		t.Fatal("third start should be blocked (limit 2)")
	}

	if !l.allow("u2", base) {
		t.Fatal("a different user must not be affected")
	}

	if !l.allow("u1", base.Add(2*time.Hour)) {
		t.Fatal("start should be allowed again after the window elapses")
	}
}

func TestFaucetSessionRateLimiterMiddleware(t *testing.T) {
	l := newFaucetSessionRateLimiter(1, time.Hour)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := l.middleware(next)

	do := func(path, user string) int {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		if user != "" {
			req = req.WithContext(withAuthUser(req.Context(), &AuthUser{Subject: user}))
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		return rec.Code
	}

	// Non-startSession calls are never limited (a claim makes many of them).
	if code := do("/faucet/net/api/powChallenge", "alice"); code != http.StatusOK {
		t.Fatalf("powChallenge should pass, got %d", code)
	}

	// startSession: first allowed, second over the limit → 429.
	if code := do("/faucet/net/api/startSession", "alice"); code != http.StatusOK {
		t.Fatalf("alice first start should pass, got %d", code)
	}
	if code := do("/faucet/net/api/startSession", "alice"); code != http.StatusTooManyRequests {
		t.Fatalf("alice second start should be 429, got %d", code)
	}

	// A different user has an independent budget.
	if code := do("/faucet/net/api/startSession", "bob"); code != http.StatusOK {
		t.Fatalf("bob first start should pass, got %d", code)
	}

	// Unauthenticated (auth disabled) is not limited here — auth is the gate.
	if code := do("/faucet/net/api/startSession", ""); code != http.StatusOK {
		t.Fatalf("unauthenticated should pass through, got %d", code)
	}
}
