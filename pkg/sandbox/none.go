package sandbox

import (
	"context"
	"errors"
	"fmt"

	"github.com/sirupsen/logrus"
)

// ErrSandboxDisabled is returned by the "none" backend for any execution or
// session operation. With sandbox.backend set to "none" the server runs
// without a container runtime, so execute_python and sandbox sessions are
// unavailable.
var ErrSandboxDisabled = errors.New(
	`sandbox.backend is "none": this panda-server runs without a Python sandbox, so execute_python and sandbox sessions are unavailable`,
)

// NoneBackend is a no-op sandbox backend for deployments that run panda-server
// without a container runtime (e.g. servers that only use the structured
// operations and never call execute_python). Start and Stop succeed as no-ops;
// every execution and session method returns ErrSandboxDisabled.
type NoneBackend struct {
	log logrus.FieldLogger
}

// NewNoneBackend creates a sandbox backend that performs no execution.
func NewNoneBackend(log logrus.FieldLogger) *NoneBackend {
	return &NoneBackend{
		log: log.WithField("component", "sandbox.none"),
	}
}

// Name returns the backend name.
func (b *NoneBackend) Name() string {
	return string(BackendNone)
}

// Start is a no-op; there is no container runtime to connect to.
func (b *NoneBackend) Start(context.Context) error {
	b.log.Info(`Sandbox disabled (backend "none"); execute_python and sandbox sessions are unavailable`)

	return nil
}

// Stop is a no-op; the backend holds no resources.
func (b *NoneBackend) Stop(context.Context) error {
	return nil
}

// Execute always fails: this backend cannot run Python code.
func (b *NoneBackend) Execute(context.Context, ExecuteRequest) (*ExecutionResult, error) {
	return nil, fmt.Errorf("cannot execute python code: %w", ErrSandboxDisabled)
}

// ListSessions always fails: this backend has no sessions.
func (b *NoneBackend) ListSessions(context.Context, string) ([]SessionInfo, error) {
	return nil, fmt.Errorf("cannot list sessions: %w", ErrSandboxDisabled)
}

// CreateSession always fails: this backend has no sessions.
func (b *NoneBackend) CreateSession(context.Context, string, map[string]string) (string, error) {
	return "", fmt.Errorf("cannot create session: %w", ErrSandboxDisabled)
}

// DestroySession always fails: this backend has no sessions.
func (b *NoneBackend) DestroySession(context.Context, string, string) error {
	return fmt.Errorf("cannot destroy session: %w", ErrSandboxDisabled)
}

// CanCreateSession reports that no session can be created, mirroring the Docker
// backend's response when sessions are disabled.
func (b *NoneBackend) CanCreateSession(context.Context, string) (bool, int, int) {
	return false, 0, 0
}

// SessionsEnabled reports that sessions are unavailable.
func (b *NoneBackend) SessionsEnabled() bool {
	return false
}
