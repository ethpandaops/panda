package sandbox

import (
	"context"
	"fmt"
)

// ListSessions returns all active sessions. If ownerID is non-empty, filters by owner.
func (b *DockerBackend) ListSessions(ctx context.Context, ownerID string) ([]SessionInfo, error) {
	if b.client == nil {
		return nil, fmt.Errorf("docker client not initialized")
	}

	if !b.sessionManager.Enabled() {
		return nil, fmt.Errorf("sessions are disabled")
	}

	containers, err := b.listAllSessionContainers(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing session containers: %w", err)
	}

	sessions := make([]SessionInfo, 0, len(containers))

	for _, c := range containers {
		// Filter by owner if specified.
		if ownerID != "" && c.OwnerID != "" && c.OwnerID != ownerID {
			continue
		}

		// Get last used time from session manager.
		lastUsed := b.sessionManager.GetLastUsed(c.SessionID)
		if lastUsed.IsZero() {
			// Session hasn't been accessed since server start, use created time.
			lastUsed = c.CreatedAt
		}

		// Collect workspace files.
		workspaceFiles := b.collectSessionFiles(ctx, c.ContainerID)

		sessions = append(sessions, SessionInfo{
			ID:             c.SessionID,
			CreatedAt:      c.CreatedAt,
			LastUsed:       lastUsed,
			TTLRemaining:   b.sessionManager.TTLRemaining(c.SessionID),
			WorkspaceFiles: workspaceFiles,
		})
	}

	return sessions, nil
}

// CreateSession creates a new empty session and returns its initial state.
func (b *DockerBackend) CreateSession(ctx context.Context, ownerID string, env map[string]string) (*CreatedSession, error) {
	if b.client == nil {
		return nil, fmt.Errorf("docker client not initialized")
	}

	if !b.sessionManager.Enabled() {
		return nil, fmt.Errorf("sessions are disabled")
	}

	// Check if we can create a new session.
	canCreate, count, maxAllowed := b.sessionManager.CanCreateSession(ctx, ownerID)
	if !canCreate {
		return nil, fmt.Errorf(
			"maximum sessions limit reached (%d/%d). Use manage_session with operation 'list' to see sessions, then 'destroy' to free up a slot",
			count, maxAllowed,
		)
	}

	// Generate session ID.
	sessionID := b.sessionManager.GenerateSessionID()

	log := b.log.WithField("session_id", sessionID).WithField("owner_id", ownerID)
	session, err := b.createManagedSession(ctx, sessionID, ownerID, env, log)
	if err != nil {
		return nil, fmt.Errorf("creating session container: %w", err)
	}

	return &CreatedSession{
		ID:           session.ID,
		TTLRemaining: b.sessionManager.TTLRemaining(session.ID),
	}, nil
}

// DestroySession destroys a session by ID.
// If ownerID is non-empty, verifies ownership before destroying.
func (b *DockerBackend) DestroySession(ctx context.Context, sessionID, ownerID string) error {
	if b.client == nil {
		return fmt.Errorf("docker client not initialized")
	}

	if !b.sessionManager.Enabled() {
		return fmt.Errorf("sessions are disabled")
	}

	return b.sessionManager.Destroy(ctx, sessionID, ownerID)
}

// CanCreateSession checks if a new session can be created.
// Returns (canCreate, currentCount, maxAllowed).
func (b *DockerBackend) CanCreateSession(ctx context.Context, ownerID string) (bool, int, int) {
	if !b.sessionManager.Enabled() {
		return false, 0, 0
	}

	return b.sessionManager.CanCreateSession(ctx, ownerID)
}

// SessionsEnabled returns whether sessions are enabled.
func (b *DockerBackend) SessionsEnabled() bool {
	return b.sessionManager.Enabled()
}
