package sandbox

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/config"
)

// emptyStore is a SessionStore with no sessions, for manager tests that don't
// exercise the store.
type emptyStore struct{}

func (emptyStore) Get(context.Context, string) (*Session, error) { return nil, nil }
func (emptyStore) List(context.Context) ([]*Session, error)      { return nil, nil }
func (emptyStore) Remove(context.Context, *Session) error        { return nil }

func TestSessionManagerStopIsIdempotent(t *testing.T) {
	enabled := true
	cfg := config.SessionConfig{
		Enabled:     &enabled,
		MaxSessions: 1,
	}

	m := NewSessionManager(cfg, logrus.New(), emptyStore{})

	ctx := context.Background()
	require.NoError(t, m.Start(ctx))

	require.NoError(t, m.Stop(ctx))
	// A second Stop must not panic on close of an already-closed channel.
	require.NotPanics(t, func() {
		_ = m.Stop(ctx)
	})
}

func TestSessionManagerRemoveSessionThenUnmarkDoesNotRecreateState(t *testing.T) {
	m := NewSessionManager(config.SessionConfig{MaxSessions: 1}, logrus.New(), emptyStore{})

	const sessionID = "session-1"

	m.RecordAccess(sessionID)
	m.markExecuting(sessionID)
	m.removeSession(sessionID)
	m.unmarkExecuting(sessionID)

	m.mu.Lock()
	defer m.mu.Unlock()

	require.NotContains(t, m.lastUsed, sessionID)
	require.NotContains(t, m.activeExecs, sessionID)
}

// laggingStore models a backend whose List does not see a session the moment
// it is created, mirroring how a freshly started container or workspace takes
// a little while to show up wherever the backend enumerates live sessions.
type laggingStore struct {
	mu       sync.Mutex
	sessions []*Session
}

func (s *laggingStore) commit(session *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.sessions = append(s.sessions, session)
}

func (s *laggingStore) List(context.Context) ([]*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]*Session, len(s.sessions))
	copy(out, s.sessions)

	return out, nil
}

func (s *laggingStore) Get(context.Context, string) (*Session, error) { return nil, nil }
func (s *laggingStore) Remove(context.Context, *Session) error        { return nil }

func TestReserveSessionHoldsCapUnderConcurrentBurst(t *testing.T) {
	const maxSessions = 3
	const burst = 12

	store := &laggingStore{}
	enabled := true
	m := NewSessionManager(config.SessionConfig{
		Enabled:     &enabled,
		MaxSessions: maxSessions,
	}, logrus.New(), store)

	ctx := context.Background()

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		created int
	)

	for i := 0; i < burst; i++ {
		wg.Add(1)

		go func(n int) {
			defer wg.Done()

			canCreate, release, _, _ := m.ReserveSession(ctx, "")
			defer release()

			if !canCreate {
				return
			}

			// The backing resource takes a moment to exist and become visible
			// to List, same as a real container starting up.
			time.Sleep(20 * time.Millisecond)
			store.commit(&Session{ID: string(rune('a' + n)), CreatedAt: time.Now()})

			mu.Lock()
			created++
			mu.Unlock()
		}(i)
	}

	wg.Wait()

	require.LessOrEqualf(t, created, maxSessions,
		"expected at most %d sessions created under a concurrent burst, got %d", maxSessions, created)
}

func TestReserveSessionReleaseFreesSlotForReuse(t *testing.T) {
	enabled := true
	m := NewSessionManager(config.SessionConfig{
		Enabled:     &enabled,
		MaxSessions: 1,
	}, logrus.New(), emptyStore{})

	ctx := context.Background()

	canCreate, release, _, _ := m.ReserveSession(ctx, "")
	require.True(t, canCreate)

	canCreate, _, count, max := m.ReserveSession(ctx, "")
	require.False(t, canCreate, "the single slot is already reserved")
	require.Equal(t, 1, count)
	require.Equal(t, 1, max)

	release()

	canCreate, release, _, _ = m.ReserveSession(ctx, "")
	require.True(t, canCreate, "releasing the first reservation should free the slot")
	release()
}
