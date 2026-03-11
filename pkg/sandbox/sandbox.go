// Package sandbox provides secure code execution in isolated containers.
package sandbox

import (
	"context"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/config"
)

// Service defines the interface for sandbox code execution backends.
type Service interface {
	// Start initializes the sandbox backend (e.g., connecting to Docker).
	Start(ctx context.Context) error
	// Stop cleans up resources and any active containers.
	Stop(ctx context.Context) error
	// Execute runs Python code in an isolated container.
	Execute(ctx context.Context, req ExecuteRequest) (*ExecutionResult, error)
	// Name returns the backend name for logging/metrics.
	Name() string

	// Session management methods.

	// ListSessions returns all active sessions. If ownerID is non-empty, filters by owner.
	ListSessions(ctx context.Context, ownerID string) ([]SessionInfo, error)
	// CreateSession creates a new empty session and returns its ID.
	CreateSession(ctx context.Context, ownerID string, env map[string]string) (string, error)
	// DestroySession destroys a session by ID.
	// If ownerID is non-empty, verifies ownership before destroying.
	DestroySession(ctx context.Context, sessionID, ownerID string) error
	// CanCreateSession checks if a new session can be created.
	// Returns (canCreate, currentCount, maxAllowed).
	CanCreateSession(ctx context.Context, ownerID string) (bool, int, int)
	// SessionsEnabled returns whether sessions are enabled.
	SessionsEnabled() bool
}

// ExecuteRequest contains the parameters for code execution.
type ExecuteRequest struct {
	// Code is the Python code to execute.
	Code string
	// Env contains additional environment variables to set in the sandbox.
	Env map[string]string
	// Timeout overrides the default execution timeout. If zero, uses the config default.
	Timeout time.Duration
	// SessionID is an optional session ID to reuse a persistent container.
	// If empty, a new session is created. If provided, the existing session is reused.
	SessionID string
	// OwnerID is the GitHub user ID that owns the session.
	// Required for session creation and verification.
	OwnerID string
}

// ExecutionResult contains the output from code execution.
type ExecutionResult struct {
	// Stdout contains the standard output from execution.
	Stdout string
	// Stderr contains the standard error from execution.
	Stderr string
	// ExitCode is the process exit code (0 = success).
	ExitCode int
	// ExecutionID is a unique identifier for this execution.
	ExecutionID string
	// OutputFiles lists file names created in /output directory.
	OutputFiles []string
	// Metrics contains any metrics reported by the executed code.
	Metrics map[string]any
	// DurationSeconds is the wall-clock execution time.
	DurationSeconds float64

	// Session-related fields (only populated when sessions are enabled).
	// SessionID is the session identifier. Can be used to reuse this session.
	SessionID string
	// SessionFiles lists files persisted in the session's /workspace directory.
	SessionFiles []SessionFile
	// SessionTTLRemaining is the time until this session expires from inactivity.
	SessionTTLRemaining time.Duration
}

// SessionFile represents a file in the session workspace.
type SessionFile struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

// SessionInfo represents information about an active session.
type SessionInfo struct {
	ID             string        `json:"session_id"`
	CreatedAt      time.Time     `json:"created_at"`
	LastUsed       time.Time     `json:"last_used"`
	TTLRemaining   time.Duration `json:"ttl_remaining"`
	WorkspaceFiles []SessionFile `json:"workspace_files"`
}

// BackendType represents the available sandbox backend types.
type BackendType string

const (
	// BackendDocker uses standard Docker containers.
	BackendDocker BackendType = "docker"
	// BackendGVisor uses Docker with gVisor runtime for enhanced isolation.
	BackendGVisor BackendType = "gvisor"
)

// New creates a new sandbox service based on the configuration.
func New(cfg config.SandboxConfig, log logrus.FieldLogger) (Service, error) {
	backendType := BackendType(cfg.Backend)

	switch backendType {
	case BackendDocker:
		return NewDockerBackend(cfg, log)
	case BackendGVisor:
		return NewGVisorBackend(cfg, log)
	default:
		return nil, fmt.Errorf("unsupported sandbox backend: %s", cfg.Backend)
	}
}

// Compile-time interface compliance checks.
var (
	_ Service = (*DockerBackend)(nil)
	_ Service = (*GVisorBackend)(nil)
)
