package sandbox

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/config"
)

// Session is the backend-agnostic record of a persistent sandbox session.
// Handle is the opaque backing resource that the store reaps and the backend
// executes in: a container ID for the docker backend, a workspace directory for
// the direct backend.
type Session struct {
	ID        string
	OwnerID   string // Optional owner ID for session binding.
	Handle    string
	CreatedAt time.Time
	LastUsed  time.Time
	Env       map[string]string
}

// SessionStore is the backend-specific half of session management: where the
// authoritative session records live and how a session's backing resource is
// reaped. All lifecycle *policy* (TTL, max-duration, ownership, the executing
// guard, limits, the cleanup loop) lives in SessionManager, which drives the
// store.
//
// This is an interface rather than an in-memory map owned by the manager
// because the backends have genuinely different sources of truth: the docker
// store queries Docker container labels (so sessions survive a server restart
// and are visible across instances), while the direct store owns an in-process
// map of workspace directories. Stores must return value snapshots, not
// pointers into their own state, so the manager can annotate the returned
// Session (e.g. LastUsed) without racing the store.
type SessionStore interface {
	// Get returns the session with the given ID, or (nil, nil) when it does not
	// exist. A non-nil error is a lookup failure, not a missing session.
	Get(ctx context.Context, sessionID string) (*Session, error)
	// List returns all live sessions.
	List(ctx context.Context) ([]*Session, error)
	// Remove tears down the session's backing resource (container / workspace).
	Remove(ctx context.Context, session *Session) error
}

// SessionManager manages the lifecycle of persistent sandbox sessions. The
// authoritative session set lives in the SessionStore; only lastUsed times and
// in-flight execution counts are kept in memory for TTL tracking. On server
// restart in-memory state is lost, so sessions get fresh TTL timers.
type SessionManager struct {
	cfg config.SessionConfig
	log logrus.FieldLogger

	// lastUsed tracks access times for TTL enforcement (best-effort, lost on restart).
	lastUsed map[string]time.Time
	// activeExecs tracks sessions with running executions to prevent TTL purging mid-execution.
	activeExecs map[string]int
	mu          sync.RWMutex

	done     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	// store is the backend-specific record persistence + resource teardown.
	store SessionStore
}

// NewSessionManager creates a new session manager backed by the given store.
func NewSessionManager(cfg config.SessionConfig, log logrus.FieldLogger, store SessionStore) *SessionManager {
	return &SessionManager{
		cfg:         cfg,
		log:         log.WithField("component", "session-manager"),
		lastUsed:    make(map[string]time.Time, cfg.MaxSessions),
		activeExecs: make(map[string]int, cfg.MaxSessions),
		done:        make(chan struct{}),
		store:       store,
	}
}

// Start begins the background cleanup goroutine.
func (m *SessionManager) Start(ctx context.Context) error {
	if !m.cfg.IsEnabled() {
		m.log.Info("Session support is disabled")
		return nil
	}

	m.log.WithFields(logrus.Fields{
		"ttl":          m.cfg.TTL,
		"max_duration": m.cfg.MaxDuration,
		"max_sessions": m.cfg.MaxSessions,
	}).Info("Starting session manager")

	m.wg.Add(1)

	go m.cleanupLoop(ctx)

	return nil
}

// Stop terminates the cleanup goroutine and destroys all active sessions.
func (m *SessionManager) Stop(ctx context.Context) error {
	if !m.cfg.IsEnabled() {
		return nil
	}

	m.log.Info("Stopping session manager")

	m.stopOnce.Do(func() {
		close(m.done)
	})
	m.wg.Wait()

	// Query all sessions and clean them up.
	sessions, err := m.store.List(ctx)
	if err != nil {
		m.log.WithError(err).Warn("Failed to list sessions during shutdown")
	} else {
		for _, s := range sessions {
			if err := m.store.Remove(ctx, s); err != nil {
				m.log.WithFields(logrus.Fields{
					"session_id": s.ID,
					"error":      err,
				}).Warn("Failed to cleanup session during shutdown")
			}
		}
	}

	// Clear state maps.
	m.mu.Lock()
	m.lastUsed = make(map[string]time.Time, m.cfg.MaxSessions)
	m.activeExecs = make(map[string]int, m.cfg.MaxSessions)
	m.mu.Unlock()

	m.log.Info("Session manager stopped")

	return nil
}

// GenerateSessionID creates a new session ID.
// The caller is responsible for associating this with a backing resource.
func (m *SessionManager) GenerateSessionID() string {
	return strings.ReplaceAll(uuid.New().String(), "-", "")[:12] // 12-char hex: 281 trillion possibilities
}

// RecordAccess records an access time for a session (for TTL tracking).
func (m *SessionManager) RecordAccess(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.lastUsed[sessionID] = time.Now()
}

// removeSession drops a session's in-memory TTL state.
// The caller is responsible for removing the underlying resource.
func (m *SessionManager) removeSession(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.lastUsed, sessionID)
	delete(m.activeExecs, sessionID)
}

// markExecuting increments the active execution count for a session.
// Sessions with active executions are protected from TTL-based purging.
func (m *SessionManager) markExecuting(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.activeExecs[sessionID]++
}

// unmarkExecuting decrements the active execution count for a session
// and refreshes the TTL so the idle timer restarts from execution completion.
func (m *SessionManager) unmarkExecuting(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.activeExecs[sessionID] <= 1 {
		delete(m.activeExecs, sessionID)
	} else {
		m.activeExecs[sessionID]--
	}

	if _, ok := m.lastUsed[sessionID]; ok {
		m.lastUsed[sessionID] = time.Now()
	}
}

// ActiveSessionCount returns the count of active sessions.
func (m *SessionManager) ActiveSessionCount(ctx context.Context) int {
	sessions, err := m.store.List(ctx)
	if err != nil {
		return 0
	}

	return len(sessions)
}

// List returns all live sessions from the store. Callers layer their own
// per-backend view (e.g. workspace files) on top.
func (m *SessionManager) List(ctx context.Context) ([]*Session, error) {
	return m.store.List(ctx)
}

// Get retrieves a session by ID and updates its last used timestamp.
// ownerID is optional - when provided, ownership is verified.
func (m *SessionManager) Get(ctx context.Context, sessionID string, ownerID string) (*Session, error) {
	if !m.cfg.IsEnabled() {
		return nil, fmt.Errorf("sessions are disabled")
	}

	session, err := m.store.Get(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("session %s not found: %w", sessionID, err)
	}

	if session == nil {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}

	// Verify ownership if ownerID is provided.
	if ownerID != "" && session.OwnerID != "" && session.OwnerID != ownerID {
		return nil, fmt.Errorf("session %s not owned by caller", sessionID)
	}

	// Check if session has exceeded max duration.
	if time.Since(session.CreatedAt) > m.cfg.MaxDuration {
		return nil, m.expireSession(session, "max duration exceeded")
	}

	// Check if session has exceeded TTL (idle timeout).
	// Sessions with active executions are protected from TTL expiry.
	// Note: On server restart, lastUsed is empty, so sessions get a fresh TTL timer.
	m.mu.RLock()
	lastUsed, hasLastUsed := m.lastUsed[sessionID]
	executing := m.activeExecs[sessionID] > 0
	m.mu.RUnlock()

	if hasLastUsed && !executing && time.Since(lastUsed) > m.cfg.TTL {
		return nil, m.expireSession(session, "idle timeout exceeded")
	}

	// Update last used timestamp.
	now := time.Now()

	m.mu.Lock()
	m.lastUsed[sessionID] = now
	m.mu.Unlock()

	session.LastUsed = now

	return session, nil
}

// TTLRemaining returns the time remaining until the session expires from inactivity.
// Returns the full TTL if session hasn't been accessed yet (e.g., after server restart).
func (m *SessionManager) TTLRemaining(sessionID string) time.Duration {
	m.mu.RLock()
	lastUsed, ok := m.lastUsed[sessionID]
	m.mu.RUnlock()

	if !ok {
		// Session hasn't been accessed since server start, return full TTL.
		return m.cfg.TTL
	}

	remaining := m.cfg.TTL - time.Since(lastUsed)

	return max(0, remaining)
}

// expireSession triggers async cleanup of an expired session and returns an error.
// Cleanup runs on a background context so it survives cancellation of the request
// ctx that triggered the expiry.
func (m *SessionManager) expireSession(session *Session, reason string) error {
	go func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := m.store.Remove(cleanupCtx, session); err != nil {
			m.log.WithFields(logrus.Fields{
				"session_id": session.ID,
				"error":      err,
			}).Warn("Failed to cleanup expired session")
		}
	}()

	m.mu.Lock()
	delete(m.lastUsed, session.ID)
	m.mu.Unlock()

	return fmt.Errorf("session %s has expired (%s)", session.ID, reason)
}

// Destroy removes a session and reaps its backing resource.
// If ownerID is non-empty, verifies ownership before destroying.
func (m *SessionManager) Destroy(ctx context.Context, sessionID, ownerID string) error {
	session, err := m.store.Get(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("session %s not found: %w", sessionID, err)
	}

	if session == nil {
		return fmt.Errorf("session %s not found", sessionID)
	}

	// Verify ownership if ownerID is provided.
	if ownerID != "" && session.OwnerID != "" && session.OwnerID != ownerID {
		return fmt.Errorf("session %s not owned by caller", sessionID)
	}

	// Drop in-memory state.
	m.mu.Lock()
	delete(m.lastUsed, sessionID)
	m.mu.Unlock()

	m.log.WithField("session_id", sessionID).Info("Destroying session")

	return m.store.Remove(ctx, session)
}

// Enabled returns whether sessions are enabled.
func (m *SessionManager) Enabled() bool {
	return m.cfg.IsEnabled()
}

// CanCreateSession checks if a new session can be created.
// If ownerID is provided, counts only sessions owned by that user.
// Returns (canCreate, currentCount, maxAllowed).
func (m *SessionManager) CanCreateSession(ctx context.Context, ownerID string) (bool, int, int) {
	if !m.cfg.IsEnabled() {
		return false, 0, 0
	}

	maxSessions := m.cfg.MaxSessions
	if maxSessions <= 0 {
		// No limit configured.
		return true, 0, 0
	}

	// Count active sessions.
	sessions, err := m.store.List(ctx)
	if err != nil {
		m.log.WithError(err).Warn("Failed to list sessions for limit check")
		// Be conservative and allow creation on error.
		return true, 0, maxSessions
	}

	// If ownerID is provided, count only sessions owned by that user.
	count := 0
	for _, s := range sessions {
		if ownerID == "" || s.OwnerID == ownerID {
			count++
		}
	}

	return count < maxSessions, count, maxSessions
}

// MaxSessions returns the configured maximum number of sessions.
func (m *SessionManager) MaxSessions() int {
	return m.cfg.MaxSessions
}

// GetLastUsed returns the last used time for a session.
// Returns the zero time if the session hasn't been accessed since server start.
func (m *SessionManager) GetLastUsed(sessionID string) time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.lastUsed[sessionID]
}

// cleanupLoop runs periodically to destroy expired sessions.
func (m *SessionManager) cleanupLoop(ctx context.Context) {
	defer m.wg.Done()

	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.done:
			return
		case <-ticker.C:
			m.cleanupExpired(ctx)
		}
	}
}

// cleanupExpired destroys sessions that have exceeded TTL or max duration.
// Lists all sessions from the store and checks expiry based on:
// - MaxDuration: from the session's CreatedAt.
// - TTL: from the in-memory lastUsed map (best-effort, fresh timer on restart).
// Sessions with active executions are protected from TTL expiry.
func (m *SessionManager) cleanupExpired(ctx context.Context) {
	sessions, err := m.store.List(ctx)
	if err != nil {
		m.log.WithError(err).Warn("Failed to list sessions for cleanup")
		return
	}

	now := time.Now()
	expired := make([]*Session, 0)

	// Snapshot lastUsed and activeExecs under the same lock to avoid TOCTOU races.
	m.mu.RLock()
	lastUsedSnapshot := make(map[string]time.Time, len(m.lastUsed))
	for k, v := range m.lastUsed {
		lastUsedSnapshot[k] = v
	}

	activeExecsSnapshot := make(map[string]int, len(m.activeExecs))
	for k, v := range m.activeExecs {
		activeExecsSnapshot[k] = v
	}
	m.mu.RUnlock()

	for _, session := range sessions {
		var reason string

		// Check max duration (from the session's creation time).
		if now.Sub(session.CreatedAt) > m.cfg.MaxDuration {
			reason = "max duration"
		} else if lastUsed, ok := lastUsedSnapshot[session.ID]; ok {
			// Check TTL (from in-memory lastUsed map).
			// Sessions with active executions are protected from TTL expiry.
			if now.Sub(lastUsed) > m.cfg.TTL && activeExecsSnapshot[session.ID] == 0 {
				reason = "TTL"
			}
		}
		// Note: If not in lastUsed map, session hasn't been accessed since server restart.
		// We don't expire these based on TTL - they get a fresh timer.

		if reason != "" {
			m.log.WithFields(logrus.Fields{
				"session_id": session.ID,
				"reason":     reason,
			}).Info("Session expired")

			expired = append(expired, session)
		}
	}

	// Remove expired sessions from lastUsed map.
	if len(expired) > 0 {
		m.mu.Lock()
		for _, session := range expired {
			delete(m.lastUsed, session.ID)
		}
		m.mu.Unlock()
	}

	// Reap expired sessions' resources.
	for _, session := range expired {
		if err := m.store.Remove(ctx, session); err != nil {
			m.log.WithFields(logrus.Fields{
				"session_id": session.ID,
				"error":      err,
			}).Warn("Failed to cleanup expired session")
		}
	}
}
