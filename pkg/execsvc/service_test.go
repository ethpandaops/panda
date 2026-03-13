package execsvc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/sandbox"
	"github.com/ethpandaops/panda/pkg/tokenstore"
)

func TestExecuteValidatesInputsAndCapacity(t *testing.T) {
	t.Parallel()

	store := tokenstore.New(time.Hour)
	t.Cleanup(store.Stop)

	svc := New(logrus.New(), &execSandboxStub{}, nil, 30, store)

	_, err := svc.Execute(context.Background(), ExecuteRequest{})
	require.EqualError(t, err, "code is required")

	_, err = svc.Execute(context.Background(), ExecuteRequest{Code: "print(1)", Timeout: MaxTimeout + 1})
	require.EqualError(t, err, "timeout must be between 1 and 600 seconds")

	svc = New(logrus.New(), &execSandboxStub{}, execEnvBuilderStub{err: errors.New("env failed")}, 30, store)
	_, err = svc.Execute(context.Background(), ExecuteRequest{Code: "print(1)"})
	require.EqualError(t, err, "failed to configure sandbox: env failed")

	svc = New(logrus.New(), &execSandboxStub{
		sessionsEnabled: true,
		canCreate:      false,
		currentCount:   2,
		maxSessions:    2,
	}, execEnvBuilderStub{env: map[string]string{"BASE": "1"}}, 30, store)
	_, err = svc.Execute(context.Background(), ExecuteRequest{Code: "print(1)", OwnerID: "user-1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "maximum sessions limit reached (2/2)")
}

func TestExecuteBuildsSandboxEnvAndRevokesRuntimeToken(t *testing.T) {
	t.Parallel()

	store := tokenstore.New(time.Hour)
	t.Cleanup(store.Stop)

	sandboxStub := &execSandboxStub{
		sessionsEnabled: true,
		canCreate:      true,
		maxSessions:    5,
		executeResult:  &sandbox.ExecutionResult{ExecutionID: "exec-1"},
	}
	svc := New(logrus.New(), sandboxStub, execEnvBuilderStub{
		env: map[string]string{"BASE": "1"},
	}, 45, store)

	result, err := svc.Execute(context.Background(), ExecuteRequest{
		Code:    "print(1)",
		OwnerID: "user-1",
	})
	require.NoError(t, err)
	assert.Equal(t, "exec-1", result.ExecutionID)
	assert.Equal(t, "print(1)", sandboxStub.executeReq.Code)
	assert.Equal(t, 45*time.Second, sandboxStub.executeReq.Timeout)
	assert.Equal(t, "1", sandboxStub.executeReq.Env["BASE"])

	token := sandboxStub.executeReq.Env["ETHPANDAOPS_API_TOKEN"]
	require.NotEmpty(t, token)
	assert.Empty(t, store.Validate(token))
}

func TestSessionHelpersDelegateToSandbox(t *testing.T) {
	t.Parallel()

	store := tokenstore.New(time.Hour)
	t.Cleanup(store.Stop)

	sandboxStub := &execSandboxStub{
		sessionsEnabled: true,
		canCreate:      true,
		maxSessions:    9,
		sessions:       []sandbox.SessionInfo{{ID: "sess-1"}},
		created:        &sandbox.CreatedSession{ID: "sess-2"},
	}
	svc := New(logrus.New(), sandboxStub, execEnvBuilderStub{
		env: map[string]string{"BASE": "1"},
	}, 30, store)

	assert.True(t, svc.SessionsEnabled())

	sessions, maxSessions, err := svc.ListSessions(context.Background(), "user-1")
	require.NoError(t, err)
	assert.Len(t, sessions, 1)
	assert.Equal(t, 9, maxSessions)

	created, err := svc.CreateSession(context.Background(), "user-1")
	require.NoError(t, err)
	assert.Equal(t, "sess-2", created.ID)
	assert.Equal(t, map[string]string{"BASE": "1"}, sandboxStub.createEnv)

	require.NoError(t, svc.DestroySession(context.Background(), "sess-1", "user-1"))
	assert.Equal(t, "sess-1", sandboxStub.destroyedSessionID)
	assert.Equal(t, "user-1", sandboxStub.destroyedOwnerID)
}

type execEnvBuilderStub struct {
	env map[string]string
	err error
}

func (s execEnvBuilderStub) BuildSandboxEnv() (map[string]string, error) {
	if s.err != nil {
		return nil, s.err
	}

	return s.env, nil
}

type execSandboxStub struct {
	sessionsEnabled    bool
	canCreate          bool
	currentCount       int
	maxSessions        int
	executeReq         sandbox.ExecuteRequest
	executeResult      *sandbox.ExecutionResult
	executeErr         error
	sessions           []sandbox.SessionInfo
	created            *sandbox.CreatedSession
	createEnv          map[string]string
	destroyedSessionID string
	destroyedOwnerID   string
}

func (s *execSandboxStub) Start(context.Context) error { return nil }

func (s *execSandboxStub) Stop(context.Context) error { return nil }

func (s *execSandboxStub) Execute(_ context.Context, req sandbox.ExecuteRequest) (*sandbox.ExecutionResult, error) {
	s.executeReq = req
	if s.executeErr != nil {
		return nil, s.executeErr
	}

	return s.executeResult, nil
}

func (s *execSandboxStub) Name() string { return "stub" }

func (s *execSandboxStub) ListSessions(context.Context, string) ([]sandbox.SessionInfo, error) {
	return s.sessions, nil
}

func (s *execSandboxStub) CreateSession(_ context.Context, _ string, env map[string]string) (*sandbox.CreatedSession, error) {
	s.createEnv = env
	return s.created, nil
}

func (s *execSandboxStub) DestroySession(_ context.Context, sessionID, ownerID string) error {
	s.destroyedSessionID = sessionID
	s.destroyedOwnerID = ownerID
	return nil
}

func (s *execSandboxStub) CanCreateSession(context.Context, string) (bool, int, int) {
	return s.canCreate, s.currentCount, s.maxSessions
}

func (s *execSandboxStub) SessionsEnabled() bool { return s.sessionsEnabled }
