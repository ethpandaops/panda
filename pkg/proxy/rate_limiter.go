package proxy

import (
	"net/http"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"golang.org/x/time/rate"

	simpleauth "github.com/ethpandaops/panda/pkg/auth"
	"github.com/ethpandaops/panda/pkg/proxy/handlers"
)

// RateLimiter provides per-user rate limiting for the proxy.
type RateLimiter struct {
	log      logrus.FieldLogger
	cfg      RateLimiterConfig
	limiters map[string]*rate.Limiter
	mu       sync.RWMutex
	stopCh   chan struct{}
	stopped  bool
}

// RateLimiterConfig configures the rate limiter.
type RateLimiterConfig struct {
	// RequestsPerMinute is the maximum requests per minute per user.
	RequestsPerMinute int

	// BurstSize is the maximum burst size.
	BurstSize int
}

// NewRateLimiter creates a new rate limiter.
func NewRateLimiter(log logrus.FieldLogger, cfg RateLimiterConfig) *RateLimiter {
	rl := &RateLimiter{
		log:      log.WithField("component", "rate-limiter"),
		cfg:      cfg,
		limiters: make(map[string]*rate.Limiter, 64),
		stopCh:   make(chan struct{}),
	}

	// Start cleanup goroutine.
	go rl.cleanupLoop()

	return rl
}

// getLimiter returns the rate limiter for the given user ID.
func (rl *RateLimiter) getLimiter(userID string) *rate.Limiter {
	rl.mu.RLock()
	limiter, ok := rl.limiters[userID]
	rl.mu.RUnlock()

	if ok {
		return limiter
	}

	// Create new limiter.
	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Double-check after acquiring write lock.
	if limiter, ok := rl.limiters[userID]; ok {
		return limiter
	}

	// Calculate rate: requests per minute -> requests per second.
	ratePerSecond := rate.Limit(float64(rl.cfg.RequestsPerMinute) / 60.0)
	limiter = rate.NewLimiter(ratePerSecond, rl.cfg.BurstSize)
	rl.limiters[userID] = limiter

	return limiter
}

// Allow checks if a request is allowed for the given user ID.
func (rl *RateLimiter) Allow(userID string) bool {
	return rl.getLimiter(userID).Allow()
}

// Middleware returns an HTTP middleware that enforces rate limiting.
func (rl *RateLimiter) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userID := GetUserID(r.Context())
			if userID == "" {
				// No user ID, allow request (auth middleware should have rejected).
				next.ServeHTTP(w, r)

				return
			}

			if !rl.Allow(userID) {
				rl.log.WithField("user_id", userID).Debug("Rate limit exceeded")

				ProxyRateLimitRejectionsTotal.WithLabelValues(extractDatasourceType(r.URL.Path)).Inc()

				w.Header().Set("Retry-After", "60")
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)

				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// Stop stops the rate limiter cleanup goroutine.
func (rl *RateLimiter) Stop() {
	rl.mu.Lock()
	if rl.stopped {
		rl.mu.Unlock()

		return
	}

	rl.stopped = true
	rl.mu.Unlock()

	close(rl.stopCh)
}

// cleanupLoop periodically removes inactive rate limiters.
func (rl *RateLimiter) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-rl.stopCh:
			return
		case <-ticker.C:
			rl.cleanup()
		}
	}
}

// cleanup removes rate limiters that have been inactive.
// A limiter is considered inactive if it has recovered to full burst capacity.
func (rl *RateLimiter) cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	for userID, limiter := range rl.limiters {
		// If the limiter has recovered to full burst, remove it.
		// This is a heuristic - if tokens == burst, user hasn't used it recently.
		if limiter.Tokens() >= float64(rl.cfg.BurstSize) {
			delete(rl.limiters, userID)
		}
	}

	rl.log.WithField("active_limiters", len(rl.limiters)).Debug("Rate limiter cleanup complete")
}

// AuditEntry represents a single audit log entry.
type AuditEntry struct {
	Subject        string   `json:"subject,omitempty"`
	Username       string   `json:"username,omitempty"`
	Groups         []string `json:"groups,omitempty"`
	Method         string   `json:"method"`
	Path           string   `json:"path"`
	DatasourceType string   `json:"datasource_type"`
	DatasourceName string   `json:"datasource_name,omitempty"`
	StatusCode     int      `json:"status_code"`
	ResponseBytes  int      `json:"response_bytes"`
	Duration       string   `json:"duration"`
	UserAgent      string   `json:"user_agent,omitempty"`
}

// Auditor logs audit entries for proxy requests.
type Auditor struct {
	log logrus.FieldLogger
	cfg AuditorConfig
}

// AuditorConfig configures the auditor.
type AuditorConfig struct{}

// NewAuditor creates a new auditor.
func NewAuditor(log logrus.FieldLogger, cfg AuditorConfig) *Auditor {
	return &Auditor{
		log: log.WithField("component", "auditor"),
		cfg: cfg,
	}
}

// Middleware returns an HTTP middleware that logs audit entries.
func (a *Auditor) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Wrap response writer to capture status code and bytes.
			wrapped := &responseCapture{ResponseWriter: w, statusCode: http.StatusOK}

			// Call next handler.
			next.ServeHTTP(wrapped, r)

			// Resolve user identity from auth context.
			proxyUser := GetAuthUser(r.Context())
			authUser := simpleauth.GetAuthUser(r.Context())

			entry := AuditEntry{
				Method:         r.Method,
				Path:           r.URL.Path,
				DatasourceType: extractDatasourceType(r.URL.Path),
				DatasourceName: r.Header.Get(handlers.DatasourceHeader),
				StatusCode:     wrapped.statusCode,
				ResponseBytes:  wrapped.bytesWritten,
				Duration:       time.Since(start).String(),
				UserAgent:      r.UserAgent(),
			}

			if proxyUser != nil {
				entry.Subject = proxyUser.Subject
				entry.Username = proxyUser.Username
				entry.Groups = append([]string(nil), proxyUser.Groups...)
			} else if authUser != nil {
				entry.Subject = authUser.Subject
				entry.Username = authUser.Username
				entry.Groups = append([]string(nil), authUser.Groups...)
			}

			// Log the audit entry.
			fields := logrus.Fields{
				"subject":         entry.Subject,
				"username":        entry.Username,
				"method":          entry.Method,
				"path":            entry.Path,
				"datasource_type": entry.DatasourceType,
				"status":          entry.StatusCode,
				"response_bytes":  entry.ResponseBytes,
				"duration":        entry.Duration,
			}

			if len(entry.Groups) > 0 {
				fields["groups"] = entry.Groups
			}

			if entry.DatasourceName != "" {
				fields["datasource_name"] = entry.DatasourceName
			}

			if entry.UserAgent != "" {
				fields["user_agent"] = entry.UserAgent
			}

			a.log.WithFields(fields).Info("Audit")
		})
	}
}
