package sandbox

import (
	"context"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/config"
)

func TestSessionManagerGetDoesNotRecordAccess(t *testing.T) {
	t.Parallel()

	createdAt := time.Now().Add(-15 * time.Minute)
	manager := newTestSessionManager(config.SessionConfig{
		TTL:         time.Hour,
		MaxDuration: 4 * time.Hour,
		MaxSessions: 4,
	}, func(context.Context, string) (*SessionContainer, error) {
		return &SessionContainer{
			ContainerID: "container-1",
			SessionID:   "session-1",
			OwnerID:     "owner-1",
			CreatedAt:   createdAt,
		}, nil
	}, nil)
	manager.now = func() time.Time { return createdAt.Add(15 * time.Minute) }

	session, err := manager.Get(context.Background(), "session-1", "owner-1")
	require.NoError(t, err)
	assert.Equal(t, createdAt, session.LastUsed)
	assert.True(t, manager.GetLastUsed("session-1").IsZero())
}

func TestSessionManagerAcquireRefreshesAccess(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.March, 11, 16, 0, 0, 0, time.UTC)
	createdAt := now.Add(-10 * time.Minute)
	manager := newTestSessionManager(config.SessionConfig{
		TTL:         time.Hour,
		MaxDuration: 4 * time.Hour,
		MaxSessions: 4,
	}, func(context.Context, string) (*SessionContainer, error) {
		return &SessionContainer{
			ContainerID: "container-1",
			SessionID:   "session-1",
			OwnerID:     "owner-1",
			CreatedAt:   createdAt,
		}, nil
	}, nil)
	manager.now = func() time.Time { return now }
	session, err := manager.Acquire(context.Background(), "session-1", "owner-1")
	require.NoError(t, err)

	lastUsed := manager.GetLastUsed("session-1")
	assert.Equal(t, now, lastUsed)
	assert.Equal(t, now, session.LastUsed)
}

func TestSessionManagerIdleExpiryHappensOnAcquire(t *testing.T) {
	t.Parallel()

	cleanupCalled := ""
	now := time.Date(2026, time.March, 11, 16, 30, 0, 0, time.UTC)
	manager := newTestSessionManager(config.SessionConfig{
		TTL:         5 * time.Minute,
		MaxDuration: 4 * time.Hour,
		MaxSessions: 4,
	}, func(context.Context, string) (*SessionContainer, error) {
		return &SessionContainer{
			ContainerID: "container-1",
			SessionID:   "session-1",
			OwnerID:     "owner-1",
			CreatedAt:   now.Add(-30 * time.Minute),
		}, nil
	}, func(_ context.Context, containerID string) error {
		cleanupCalled = containerID
		return nil
	})
	manager.now = func() time.Time { return now }
	manager.cleanupExecutor = func(fn func()) { fn() }

	manager.mu.Lock()
	manager.lastUsed["session-1"] = now.Add(-30 * time.Minute)
	manager.mu.Unlock()

	session, err := manager.Get(context.Background(), "session-1", "owner-1")
	require.NoError(t, err)
	assert.Equal(t, "session-1", session.ID)

	_, err = manager.Acquire(context.Background(), "session-1", "owner-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "idle timeout exceeded")
	assert.Equal(t, "container-1", cleanupCalled)
}

func TestSessionManagerTTLRemainingDefaultsToFullTTL(t *testing.T) {
	t.Parallel()

	manager := newTestSessionManager(config.SessionConfig{
		TTL:         10 * time.Minute,
		MaxDuration: time.Hour,
		MaxSessions: 4,
	}, nil, nil)

	assert.Equal(t, 10*time.Minute, manager.TTLRemaining("unknown"))
}

func TestSessionManagerGetRejectsWrongOwner(t *testing.T) {
	t.Parallel()

	manager := newTestSessionManager(config.SessionConfig{
		TTL:         time.Hour,
		MaxDuration: 4 * time.Hour,
		MaxSessions: 4,
	}, func(context.Context, string) (*SessionContainer, error) {
		return &SessionContainer{
			ContainerID: "container-1",
			SessionID:   "session-1",
			OwnerID:     "owner-1",
			CreatedAt:   time.Now(),
		}, nil
	}, nil)

	_, err := manager.Get(context.Background(), "session-1", "owner-2")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not owned by caller")
}

func TestSessionManagerCleanupExpiredUsesInjectedClock(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.March, 11, 18, 0, 0, 0, time.UTC)
	cleaned := []string{}
	manager := newTestSessionManagerWithListAll(
		config.SessionConfig{
			TTL:         5 * time.Minute,
			MaxDuration: time.Hour,
			MaxSessions: 4,
		},
		nil,
		func(context.Context) ([]*SessionContainer, error) {
			return []*SessionContainer{
				{
					ContainerID: "container-max",
					SessionID:   "session-max",
					OwnerID:     "owner-1",
					CreatedAt:   now.Add(-2 * time.Hour),
				},
				{
					ContainerID: "container-ttl",
					SessionID:   "session-ttl",
					OwnerID:     "owner-1",
					CreatedAt:   now.Add(-10 * time.Minute),
				},
			}, nil
		},
		func(_ context.Context, containerID string) error {
			cleaned = append(cleaned, containerID)
			return nil
		},
	)
	manager.now = func() time.Time { return now }
	manager.RecordAccess("session-ttl")
	manager.mu.Lock()
	manager.lastUsed["session-ttl"] = now.Add(-10 * time.Minute)
	manager.mu.Unlock()

	manager.cleanupExpired(context.Background())

	assert.ElementsMatch(t, []string{"container-max", "container-ttl"}, cleaned)
	assert.True(t, manager.GetLastUsed("session-max").IsZero())
	assert.True(t, manager.GetLastUsed("session-ttl").IsZero())
}

func TestSessionManagerStopCleansUpContainersAndResetsLastUsed(t *testing.T) {
	t.Parallel()

	cleaned := []string{}
	manager := newTestSessionManagerWithListAll(
		config.SessionConfig{
			TTL:         time.Hour,
			MaxDuration: 4 * time.Hour,
			MaxSessions: 4,
		},
		nil,
		func(context.Context) ([]*SessionContainer, error) {
			return []*SessionContainer{
				{ContainerID: "container-1", SessionID: "session-1"},
				{ContainerID: "container-2", SessionID: "session-2"},
			}, nil
		},
		func(_ context.Context, containerID string) error {
			cleaned = append(cleaned, containerID)
			return nil
		},
	)
	manager.RecordAccess("session-1")
	manager.RecordAccess("session-2")

	err := manager.Stop(context.Background())
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{"container-1", "container-2"}, cleaned)
	assert.True(t, manager.GetLastUsed("session-1").IsZero())
	assert.True(t, manager.GetLastUsed("session-2").IsZero())
}

func newTestSessionManager(
	cfg config.SessionConfig,
	containerLister ContainerLister,
	cleanup func(context.Context, string) error,
) *SessionManager {
	return newTestSessionManagerWithListAll(
		cfg,
		containerLister,
		func(context.Context) ([]*SessionContainer, error) { return nil, nil },
		cleanup,
	)
}

func newTestSessionManagerWithListAll(
	cfg config.SessionConfig,
	containerLister ContainerLister,
	containerListAll ContainerListAll,
	cleanup func(context.Context, string) error,
) *SessionManager {
	if containerLister == nil {
		containerLister = func(context.Context, string) (*SessionContainer, error) { return nil, nil }
	}

	if containerListAll == nil {
		containerListAll = func(context.Context) ([]*SessionContainer, error) { return nil, nil }
	}

	if cleanup == nil {
		cleanup = func(context.Context, string) error { return nil }
	}

	return NewSessionManager(
		cfg,
		logrus.New(),
		containerLister,
		containerListAll,
		cleanup,
	)
}
