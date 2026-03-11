package execsvc

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/mcp/pkg/config"
	"github.com/ethpandaops/mcp/pkg/module"
	"github.com/ethpandaops/mcp/pkg/sandbox"
	"github.com/ethpandaops/mcp/pkg/tokenstore"
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
	log           logrus.FieldLogger
	sandboxSvc    sandbox.Service
	cfg           *config.Config
	moduleReg     *module.Registry
	runtimeTokens *tokenstore.Store
}

// New creates a new execution service.
func New(
	log logrus.FieldLogger,
	sandboxSvc sandbox.Service,
	cfg *config.Config,
	moduleReg *module.Registry,
	runtimeTokens *tokenstore.Store,
) *Service {
	return &Service{
		log:           log.WithField("component", "exec-service"),
		sandboxSvc:    sandboxSvc,
		cfg:           cfg,
		moduleReg:     moduleReg,
		runtimeTokens: runtimeTokens,
	}
}

// Execute runs code in the sandbox.
func (s *Service) Execute(ctx context.Context, req ExecuteRequest) (*sandbox.ExecutionResult, error) {
	if req.Code == "" {
		return nil, fmt.Errorf("code is required")
	}

	timeout := req.Timeout
	if timeout == 0 {
		timeout = s.cfg.Sandbox.Timeout
	}

	if timeout < MinTimeout || timeout > MaxTimeout {
		return nil, fmt.Errorf("timeout must be between %d and %d seconds", MinTimeout, MaxTimeout)
	}

	env, err := s.BuildSandboxEnv()
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
func (s *Service) CreateSession(ctx context.Context, ownerID string) (string, error) {
	env, err := s.BuildSandboxEnv()
	if err != nil {
		return "", fmt.Errorf("building sandbox env: %w", err)
	}

	return s.sandboxSvc.CreateSession(ctx, ownerID, env)
}

// DestroySession destroys a persistent sandbox session.
func (s *Service) DestroySession(ctx context.Context, sessionID, ownerID string) error {
	return s.sandboxSvc.DestroySession(ctx, sessionID, ownerID)
}

// BuildSandboxEnv collects environment variables from all initialized modules
// and adds the sandbox API URL.
func (s *Service) BuildSandboxEnv() (map[string]string, error) {
	env, err := s.moduleReg.SandboxEnv()
	if err != nil {
		return nil, fmt.Errorf("collecting sandbox env: %w", err)
	}

	apiURL := sandboxAPIURL(s.cfg)
	if apiURL == "" {
		return nil, fmt.Errorf("server.sandbox_url or server.base_url is required for sandbox API access")
	}

	env["ETHPANDAOPS_API_URL"] = apiURL

	return env, nil
}

func sandboxAPIURL(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}

	if value := strings.TrimSpace(cfg.Server.SandboxURL); value != "" {
		return strings.TrimRight(value, "/")
	}

	if value := strings.TrimSpace(cfg.Server.BaseURL); value != "" {
		return strings.TrimRight(value, "/")
	}

	if value := strings.TrimSpace(cfg.Server.URL); value != "" {
		return strings.TrimRight(value, "/")
	}

	return ""
}
