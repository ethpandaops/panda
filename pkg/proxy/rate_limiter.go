package proxy

import (
	"bytes"
	"io"
	"net/http"
	"strings"
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

				user, org := resolveUserLabels(r.Context())
				ProxyRateLimitRejectionsTotal.WithLabelValues(user, org).Inc()

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
	GitHubLogin    string   `json:"github_login"`
	GitHubID       int64    `json:"github_id"`
	Orgs           []string `json:"orgs,omitempty"`
	Method         string   `json:"method"`
	Path           string   `json:"path"`
	DatasourceType string   `json:"datasource_type"`
	DatasourceName string   `json:"datasource_name,omitempty"`
	Query          string   `json:"query,omitempty"`
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
type AuditorConfig struct {
	// LogQueries controls whether to log query content.
	LogQueries bool

	// MaxQueryLength is the maximum length of query to log.
	MaxQueryLength int
}

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

			// Capture the request body before the downstream handler consumes it.
			// Skip for S3 routes since those are binary file uploads.
			var bodySnapshot string

			if a.cfg.LogQueries && r.Body != nil && !isS3Route(r.URL.Path) {
				bodySnapshot = captureBody(r, a.cfg.MaxQueryLength)
			}

			// Wrap response writer to capture status code and bytes.
			wrapped := &responseCapture{ResponseWriter: w, statusCode: http.StatusOK}

			// Call next handler.
			next.ServeHTTP(wrapped, r)

			// Resolve user identity from auth context.
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

			if authUser != nil {
				entry.GitHubLogin = authUser.GitHubLogin
				entry.GitHubID = authUser.GitHubID
				entry.Orgs = authUser.Orgs
			}

			// Add query if configured.
			if a.cfg.LogQueries {
				query := extractQuery(r, bodySnapshot)
				if len(query) > a.cfg.MaxQueryLength {
					query = query[:a.cfg.MaxQueryLength] + "..."
				}

				entry.Query = query
			}

			// Log the audit entry.
			fields := logrus.Fields{
				"github_login":    entry.GitHubLogin,
				"github_id":       entry.GitHubID,
				"method":          entry.Method,
				"path":            entry.Path,
				"datasource_type": entry.DatasourceType,
				"status":          entry.StatusCode,
				"response_bytes":  entry.ResponseBytes,
				"duration":        entry.Duration,
			}

			if len(entry.Orgs) > 0 {
				fields["orgs"] = entry.Orgs
			}

			if entry.DatasourceName != "" {
				fields["datasource_name"] = entry.DatasourceName
			}

			if entry.Query != "" {
				fields["query"] = entry.Query
			}

			if entry.UserAgent != "" {
				fields["user_agent"] = entry.UserAgent
			}

			a.log.WithFields(fields).Info("Audit")
		})
	}
}

// captureBody reads up to maxLen bytes from the request body and replaces it
// with a new reader so downstream handlers can still consume it.
func captureBody(r *http.Request, maxLen int) string {
	// Read up to maxLen+1 to detect truncation without reading the entire body.
	limit := int64(maxLen + 1)

	buf, err := io.ReadAll(io.LimitReader(r.Body, limit))
	if err != nil || len(buf) == 0 {
		// Restore body even on error.
		r.Body = io.NopCloser(bytes.NewReader(buf))

		return ""
	}

	// Restore the full body for downstream handlers.
	remaining, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(buf), bytes.NewReader(remaining)))

	return strings.TrimSpace(string(buf))
}

// isS3Route returns true if the path is an S3 route (binary uploads).
func isS3Route(path string) bool {
	return len(path) > 3 && path[:4] == "/s3/"
}

// extractQuery extracts query content from the request.
// It checks URL query parameters first, then falls back to the captured POST body.
func extractQuery(r *http.Request, bodySnapshot string) string {
	// Try URL query parameter first (used by all datasources for GET requests).
	if q := r.URL.Query().Get("query"); q != "" {
		return q
	}

	// Fall back to captured POST body (ClickHouse sends SQL as POST body).
	if bodySnapshot != "" {
		return bodySnapshot
	}

	return ""
}
