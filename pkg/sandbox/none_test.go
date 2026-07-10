package sandbox

import (
	"context"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/config"
)

func TestNoneBackendConstructsViaNew(t *testing.T) {
	svc, err := New(config.SandboxConfig{Backend: "none"}, logrus.New())
	require.NoError(t, err)
	require.NotNil(t, svc)
	assert.Equal(t, "none", svc.Name())
}

func TestNoneBackendStartStopAreNoOps(t *testing.T) {
	svc, err := New(config.SandboxConfig{Backend: "none"}, logrus.New())
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, svc.Start(ctx))
	require.NoError(t, svc.Stop(ctx))
}

func TestNoneBackendExecutionReturnsErrSandboxDisabled(t *testing.T) {
	svc, err := New(config.SandboxConfig{Backend: "none"}, logrus.New())
	require.NoError(t, err)

	ctx := context.Background()

	_, execErr := svc.Execute(ctx, ExecuteRequest{Code: "print('hi')"})
	require.ErrorIs(t, execErr, ErrSandboxDisabled)

	_, listErr := svc.ListSessions(ctx, "")
	require.ErrorIs(t, listErr, ErrSandboxDisabled)

	_, createErr := svc.CreateSession(ctx, "", nil)
	require.ErrorIs(t, createErr, ErrSandboxDisabled)

	destroyErr := svc.DestroySession(ctx, "session-1", "")
	require.ErrorIs(t, destroyErr, ErrSandboxDisabled)
}

func TestNoneBackendSessionsDisabled(t *testing.T) {
	svc, err := New(config.SandboxConfig{Backend: "none"}, logrus.New())
	require.NoError(t, err)

	assert.False(t, svc.SessionsEnabled())

	canCreate, count, maxAllowed := svc.CanCreateSession(context.Background(), "")
	assert.False(t, canCreate)
	assert.Zero(t, count)
	assert.Zero(t, maxAllowed)
}

func TestNewRejectsUnsupportedBackend(t *testing.T) {
	_, err := New(config.SandboxConfig{Backend: "bogus"}, logrus.New())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported sandbox backend")
}
