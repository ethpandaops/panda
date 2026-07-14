package sandbox

import (
	"context"
	"testing"

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
