package sandbox

import (
	"context"
	"errors"
)

// errDisabled is returned by the no-op backend's execution methods.
var errDisabled = errors.New("sandbox is disabled (sandbox.backend: none); code execution is unavailable")

// noopBackend is a sandbox backend that does nothing. It lets the server run
// without a code-execution sandbox — useful for a lean local-dev server focused
// on features that don't need code execution (e.g. `panda devnet`). Execution
// and session methods fail cleanly rather than panicking.
type noopBackend struct{}

// NewNoopBackend returns a disabled sandbox backend.
func NewNoopBackend() Service { return &noopBackend{} }

func (b *noopBackend) Start(context.Context) error { return nil }
func (b *noopBackend) Stop(context.Context) error  { return nil }
func (b *noopBackend) Name() string                { return "none" }

func (b *noopBackend) Execute(context.Context, ExecuteRequest) (*ExecutionResult, error) {
	return nil, errDisabled
}

func (b *noopBackend) ListSessions(context.Context, string) ([]SessionInfo, error) {
	return nil, errDisabled
}

func (b *noopBackend) CreateSession(context.Context, string, map[string]string) (string, error) {
	return "", errDisabled
}

func (b *noopBackend) DestroySession(context.Context, string, string) error {
	return errDisabled
}

func (b *noopBackend) CanCreateSession(context.Context, string) (bool, int, int) {
	return false, 0, 0
}

func (b *noopBackend) SessionsEnabled() bool { return false }
