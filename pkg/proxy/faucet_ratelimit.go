package proxy

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Behind the proxy every faucet request carries the proxy's IP, so the faucet's
// own per-IP concurrency/recurring limits collapse to a single global bucket.
// These restore per-user fairness at the proxy, which is the only party that
// knows the authenticated identity.
const (
	faucetSessionLimit  = 30
	faucetSessionWindow = time.Hour
)

// faucetSessionRateLimiter caps how many faucet sessions an authenticated user
// may start within a rolling window.
type faucetSessionRateLimiter struct {
	limit  int
	window time.Duration

	mu   sync.Mutex
	seen map[string][]time.Time
}

func newFaucetSessionRateLimiter(limit int, window time.Duration) *faucetSessionRateLimiter {
	return &faucetSessionRateLimiter{
		limit:  limit,
		window: window,
		seen:   make(map[string][]time.Time),
	}
}

// allow records a session start for user at now and reports whether it is under
// the limit within the window. Timestamps older than the window are pruned.
func (l *faucetSessionRateLimiter) allow(user string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	cutoff := now.Add(-l.window)

	kept := l.seen[user][:0]
	for _, t := range l.seen[user] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}

	if len(kept) >= l.limit {
		l.seen[user] = kept

		return false
	}

	l.seen[user] = append(kept, now)

	return true
}

// middleware rate-limits POST /faucet/{network}/api/startSession per
// authenticated user; every other faucet request passes through untouched (a
// single claim makes many powChallenge/powSubmit calls we must not count).
// Unauthenticated requests (auth disabled) are not limited here — the auth
// middleware is the gate in that case.
func (l *faucetSessionRateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/api/startSession") {
			next.ServeHTTP(w, r)

			return
		}

		if user := faucetUserKey(r.Context()); user != "" && !l.allow(user, time.Now()) {
			http.Error(w, "faucet session rate limit exceeded for this user", http.StatusTooManyRequests)

			return
		}

		next.ServeHTTP(w, r)
	})
}

// faucetUserKey returns a stable identity for the authenticated caller.
func faucetUserKey(ctx context.Context) string {
	user := GetAuthUser(ctx)
	if user == nil {
		return ""
	}

	if user.Subject != "" {
		return user.Subject
	}

	return user.Username
}
