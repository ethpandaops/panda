package execsvc

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/sandbox"
	"github.com/ethpandaops/panda/pkg/tokenstore"
)

const (
	MinTimeout = 1
	MaxTimeout = 600
)

// ExecuteRequest describes a sandbox execution request.
type ExecuteRequest struct {
	Code      string
	Timeout   int
	SessionID string
	OwnerID   string
}

// Service orchestrates sandbox execution with module-provided env and runtime tokens.
type Service struct {
	log            logrus.FieldLogger
	sandboxSvc     sandbox.Service
	envBuilder     SandboxEnvBuilder
	defaultTimeout int
	runtimeTokens  *tokenstore.Store
}

// SandboxEnvBuilder provides the shared sandbox execution environment.
type SandboxEnvBuilder interface {
	BuildSandboxEnv() (map[string]string, error)
}

// New creates a new execution service.
func New(
	log logrus.FieldLogger,
	sandboxSvc sandbox.Service,
	envBuilder SandboxEnvBuilder,
	defaultTimeout int,
	runtimeTokens *tokenstore.Store,
) *Service {
	return &Service{
		log:            log.WithField("component", "exec-service"),
		sandboxSvc:     sandboxSvc,
		envBuilder:     envBuilder,
		defaultTimeout: defaultTimeout,
		runtimeTokens:  runtimeTokens,
	}
}

// Execute runs code in the sandbox.
func (s *Service) Execute(ctx context.Context, req ExecuteRequest) (*sandbox.ExecutionResult, error) {
	if req.Code == "" {
		return nil, fmt.Errorf("code is required")
	}

	timeout := req.Timeout
	if timeout == 0 {
		timeout = s.defaultTimeout
	}

	if timeout < MinTimeout || timeout > MaxTimeout {
		return nil, fmt.Errorf("timeout must be between %d and %d seconds", MinTimeout, MaxTimeout)
	}

	env, err := s.buildSandboxEnv()
	if err != nil {
		return nil, fmt.Errorf("failed to configure sandbox: %w", err)
	}

	executionID := uuid.New().String()
	runtimeToken := s.runtimeTokens.Register(executionID)
	env["ETHPANDAOPS_API_TOKEN"] = runtimeToken
	defer s.runtimeTokens.Revoke(executionID)

	if req.SessionID == "" && s.sandboxSvc.SessionsEnabled() {
		canCreate, count, maxAllowed := s.sandboxSvc.CanCreateSession(ctx, req.OwnerID)
		if !canCreate {
			return nil, fmt.Errorf(
				"maximum sessions limit reached (%d/%d). Use manage_session with operation 'list' to see sessions, then 'destroy' to free up a slot",
				count,
				maxAllowed,
			)
		}
	}

	return s.sandboxSvc.Execute(ctx, sandbox.ExecuteRequest{
		Code:      req.Code,
		Env:       env,
		Timeout:   time.Duration(timeout) * time.Second,
		SessionID: req.SessionID,
		OwnerID:   req.OwnerID,
	})
}

// SessionsEnabled reports whether the sandbox supports persistent sessions.
func (s *Service) SessionsEnabled() bool {
	return s.sandboxSvc.SessionsEnabled()
}

// ListSessions returns all sessions for the given owner.
func (s *Service) ListSessions(ctx context.Context, ownerID string) ([]sandbox.SessionInfo, int, error) {
	sessions, err := s.sandboxSvc.ListSessions(ctx, ownerID)
	if err != nil {
		return nil, 0, err
	}

	_, _, maxSessions := s.sandboxSvc.CanCreateSession(ctx, ownerID)

	return sessions, maxSessions, nil
}

// CreateSession creates a new persistent sandbox session.
func (s *Service) CreateSession(ctx context.Context, ownerID string) (*sandbox.CreatedSession, error) {
	env, err := s.buildSandboxEnv()
	if err != nil {
		return nil, fmt.Errorf("building sandbox env: %w", err)
	}

	return s.sandboxSvc.CreateSession(ctx, ownerID, env)
}

// DestroySession destroys a persistent sandbox session.
func (s *Service) DestroySession(ctx context.Context, sessionID, ownerID string) error {
	return s.sandboxSvc.DestroySession(ctx, sessionID, ownerID)
}

func (s *Service) buildSandboxEnv() (map[string]string, error) {
	if s.envBuilder == nil {
		return nil, fmt.Errorf("sandbox env builder is required")
	}

	return s.envBuilder.BuildSandboxEnv()
}
