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

// Session represents a persistent sandbox execution environment.
// This is a transient view constructed from container state, not stored in memory.
type Session struct {
	ID          string
	OwnerID     string // Optional owner ID for session binding
	ContainerID string
	CreatedAt   time.Time
	LastUsed    time.Time
	Env         map[string]string
}

// SessionContainer represents container metadata for session lookup.
type SessionContainer struct {
	ContainerID string
	SessionID   string
	OwnerID     string
	CreatedAt   time.Time
}

// ContainerLister queries Docker for session containers.
type ContainerLister func(ctx context.Context, sessionID string) (*SessionContainer, error)

// ContainerListAll lists all session containers for cleanup.
type ContainerListAll func(ctx context.Context) ([]*SessionContainer, error)

// SessionManager manages the lifecycle of persistent sandbox sessions.
// Session state is stored in container labels; only lastUsed times are kept in memory
// for TTL tracking. On server restart, sessions survive but get fresh TTL timers.
type SessionManager struct {
	cfg config.SessionConfig
	log logrus.FieldLogger
	now func() time.Time

	// lastUsed tracks access times for TTL enforcement (best-effort, lost on restart).
	lastUsed map[string]time.Time
	mu       sync.RWMutex

	done chan struct{}
	wg   sync.WaitGroup

	// containerLister queries Docker for a session container by ID.
	containerLister ContainerLister
	// containerListAll lists all session containers for cleanup.
	containerListAll ContainerListAll
	// cleanupCallback is called when a session is destroyed.
	cleanupCallback func(ctx context.Context, containerID string) error
	// cleanupExecutor runs async cleanup work. Tests can override this to make cleanup deterministic.
	cleanupExecutor func(func())
}

// NewSessionManager creates a new session manager.
func NewSessionManager(
	cfg config.SessionConfig,
	log logrus.FieldLogger,
	containerLister ContainerLister,
	containerListAll ContainerListAll,
	cleanupCallback func(ctx context.Context, containerID string) error,
) *SessionManager {
	return &SessionManager{
		cfg:              cfg,
		log:              log.WithField("component", "session-manager"),
		now:              time.Now,
		lastUsed:         make(map[string]time.Time, cfg.MaxSessions),
		done:             make(chan struct{}),
		containerLister:  containerLister,
		containerListAll: containerListAll,
		cleanupCallback:  cleanupCallback,
		cleanupExecutor: func(fn func()) {
			go fn()
		},
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

	close(m.done)
	m.wg.Wait()

	// Query all session containers and clean them up.
	containers, err := m.containerListAll(ctx)
	if err != nil {
		m.log.WithError(err).Warn("Failed to list session containers during shutdown")
	} else {
		for _, c := range containers {
			if err := m.cleanupCallback(ctx, c.ContainerID); err != nil {
				m.log.WithFields(logrus.Fields{
					"session_id":   c.SessionID,
					"container_id": c.ContainerID,
					"error":        err,
				}).Warn("Failed to cleanup session during shutdown")
			}
		}
	}

	// Clear lastUsed map.
	m.mu.Lock()
	m.lastUsed = make(map[string]time.Time, m.cfg.MaxSessions)
	m.mu.Unlock()

	m.log.Info("Session manager stopped")

	return nil
}

// GenerateSessionID creates a new session ID.
// The caller is responsible for setting this on the container label.
func (m *SessionManager) GenerateSessionID() string {
	return strings.ReplaceAll(uuid.New().String(), "-", "")[:12] // 12-char hex: 281 trillion possibilities
}

// RecordAccess records an access time for a session (for TTL tracking).
func (m *SessionManager) RecordAccess(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.lastUsed[sessionID] = m.now()
}

// ActiveSessionCount returns the count of active sessions by querying Docker.
func (m *SessionManager) ActiveSessionCount(ctx context.Context) int {
	containers, err := m.containerListAll(ctx)
	if err != nil {
		return 0
	}

	return len(containers)
}

// Get retrieves a session by ID without mutating TTL/access state.
// ownerID is optional - when provided, ownership is verified.
// Session state is queried from Docker; only lastUsed is tracked in memory.
func (m *SessionManager) Get(ctx context.Context, sessionID string, ownerID string) (*Session, error) {
	if !m.cfg.IsEnabled() {
		return nil, fmt.Errorf("sessions are disabled")
	}

	container, err := m.lookupContainer(ctx, sessionID, ownerID)
	if err != nil {
		return nil, err
	}

	return m.sessionFromContainer(container), nil
}

// Acquire retrieves a session, enforces expiry, and records access for active use.
func (m *SessionManager) Acquire(ctx context.Context, sessionID string, ownerID string) (*Session, error) {
	session, err := m.Get(ctx, sessionID, ownerID)
	if err != nil {
		return nil, err
	}

	now := m.now()
	if now.Sub(session.CreatedAt) > m.cfg.MaxDuration {
		return nil, m.expireSession(session.ID, session.ContainerID, "max duration exceeded")
	}

	lastUsed, hasLastUsed := m.lastAccess(session.ID)
	if hasLastUsed && now.Sub(lastUsed) > m.cfg.TTL {
		return nil, m.expireSession(session.ID, session.ContainerID, "idle timeout exceeded")
	}

	m.RecordAccess(session.ID)
	session.LastUsed = now

	return session, nil
}

// TTLRemaining returns the time remaining until the session expires from inactivity.
// Returns the full TTL if session hasn't been accessed yet (e.g., after server restart).
func (m *SessionManager) TTLRemaining(sessionID string) time.Duration {
	lastUsed, ok := m.lastAccess(sessionID)

	if !ok {
		// Session hasn't been accessed since server start, return full TTL.
		return m.cfg.TTL
	}

	remaining := m.cfg.TTL - m.now().Sub(lastUsed)

	return max(0, remaining)
}

func (m *SessionManager) lookupContainer(ctx context.Context, sessionID, ownerID string) (*SessionContainer, error) {
	container, err := m.containerLister(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("session %s not found: %w", sessionID, err)
	}

	if container == nil {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}

	if ownerID != "" && container.OwnerID != "" && container.OwnerID != ownerID {
		return nil, fmt.Errorf("session %s not owned by caller", sessionID)
	}

	return container, nil
}

func (m *SessionManager) sessionFromContainer(container *SessionContainer) *Session {
	lastUsed, ok := m.lastAccess(container.SessionID)
	if !ok {
		lastUsed = container.CreatedAt
	}

	return &Session{
		ID:          container.SessionID,
		OwnerID:     container.OwnerID,
		ContainerID: container.ContainerID,
		CreatedAt:   container.CreatedAt,
		LastUsed:    lastUsed,
	}
}

func (m *SessionManager) lastAccess(sessionID string) (time.Time, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	lastUsed, ok := m.lastUsed[sessionID]
	return lastUsed, ok
}

// expireSession triggers async cleanup of an expired session and returns an error.
// This consolidates the common pattern of async cleanup + lastUsed removal + error return.
func (m *SessionManager) expireSession(sessionID, containerID, reason string) error {
	m.cleanupExecutor(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := m.cleanupCallback(cleanupCtx, containerID); err != nil {
			m.log.WithFields(logrus.Fields{
				"session_id":   sessionID,
				"container_id": containerID,
				"error":        err,
			}).Warn("Failed to cleanup expired session container")
		}
	})

	m.mu.Lock()
	delete(m.lastUsed, sessionID)
	m.mu.Unlock()

	return fmt.Errorf("session %s has expired (%s)", sessionID, reason)
}

// Destroy removes a session and triggers cleanup callback.
// If ownerID is non-empty, verifies ownership before destroying.
func (m *SessionManager) Destroy(ctx context.Context, sessionID, ownerID string) error {
	// Query Docker for the session container.
	container, err := m.containerLister(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("session %s not found: %w", sessionID, err)
	}

	if container == nil {
		return fmt.Errorf("session %s not found", sessionID)
	}

	// Verify ownership if ownerID is provided.
	if ownerID != "" && container.OwnerID != "" && container.OwnerID != ownerID {
		return fmt.Errorf("session %s not owned by caller", sessionID)
	}

	// Remove from lastUsed map.
	m.mu.Lock()
	delete(m.lastUsed, sessionID)
	m.mu.Unlock()

	m.log.WithFields(logrus.Fields{
		"session_id":   sessionID,
		"container_id": container.ContainerID,
	}).Info("Destroying session")

	return m.cleanupCallback(ctx, container.ContainerID)
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
	containers, err := m.containerListAll(ctx)
	if err != nil {
		m.log.WithError(err).Warn("Failed to list session containers for limit check")
		// Be conservative and allow creation on error.
		return true, 0, maxSessions
	}

	// If ownerID is provided, count only sessions owned by that user.
	count := 0
	for _, c := range containers {
		if ownerID == "" || c.OwnerID == ownerID {
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
// Queries Docker for all session containers and checks expiry based on:
// - MaxDuration: from container's CreatedAt label
// - TTL: from in-memory lastUsed map (best-effort, sessions get fresh TTL on restart)
func (m *SessionManager) cleanupExpired(ctx context.Context) {
	// Query all session containers from Docker.
	containers, err := m.containerListAll(ctx)
	if err != nil {
		m.log.WithError(err).Warn("Failed to list session containers for cleanup")
		return
	}

	now := m.now()
	expiredContainers := make([]*SessionContainer, 0)

	// Get a snapshot of lastUsed times.
	m.mu.RLock()
	lastUsedSnapshot := make(map[string]time.Time, len(m.lastUsed))
	for k, v := range m.lastUsed {
		lastUsedSnapshot[k] = v
	}
	m.mu.RUnlock()

	for _, container := range containers {
		var reason string

		// Check max duration (from container label).
		if now.Sub(container.CreatedAt) > m.cfg.MaxDuration {
			reason = "max duration"
		} else if lastUsed, ok := lastUsedSnapshot[container.SessionID]; ok {
			// Check TTL (from in-memory lastUsed map).
			if now.Sub(lastUsed) > m.cfg.TTL {
				reason = "TTL"
			}
		}
		// Note: If not in lastUsed map, session hasn't been accessed since server restart.
		// We don't expire these based on TTL - they get a fresh timer.

		if reason != "" {
			m.log.WithFields(logrus.Fields{
				"session_id": container.SessionID,
				"reason":     reason,
			}).Info("Session expired")

			expiredContainers = append(expiredContainers, container)
		}
	}

	// Remove expired sessions from lastUsed map.
	if len(expiredContainers) > 0 {
		m.mu.Lock()
		for _, container := range expiredContainers {
			delete(m.lastUsed, container.SessionID)
		}
		m.mu.Unlock()
	}

	// Cleanup containers.
	for _, container := range expiredContainers {
		if err := m.cleanupCallback(ctx, container.ContainerID); err != nil {
			m.log.WithFields(logrus.Fields{
				"session_id":   container.SessionID,
				"container_id": container.ContainerID,
				"error":        err,
			}).Warn("Failed to cleanup expired session")
		}
	}
}
