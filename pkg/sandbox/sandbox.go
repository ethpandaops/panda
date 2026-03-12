// Package sandbox provides secure code execution in isolated containers.
package sandbox

import (
	"context"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/config"
)

// Service runs code in an isolated backend and optionally manages persistent sessions.
type Service interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Execute(ctx context.Context, req ExecuteRequest) (*ExecutionResult, error)
	Name() string
	ListSessions(ctx context.Context, ownerID string) ([]SessionInfo, error)
	CreateSession(ctx context.Context, ownerID string, env map[string]string) (*CreatedSession, error)
	DestroySession(ctx context.Context, sessionID, ownerID string) error
	CanCreateSession(ctx context.Context, ownerID string) (bool, int, int)
	SessionsEnabled() bool
}

type ExecuteRequest struct {
	Code      string
	Env       map[string]string
	Timeout   time.Duration
	SessionID string
	OwnerID   string
}

type ExecutionResult struct {
	Stdout              string
	Stderr              string
	ExitCode            int
	ExecutionID         string
	OutputFiles         []string
	Metrics             map[string]any
	DurationSeconds     float64
	SessionID           string
	SessionFiles        []SessionFile
	SessionTTLRemaining time.Duration
}

type SessionFile struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

type SessionInfo struct {
	ID             string        `json:"session_id"`
	CreatedAt      time.Time     `json:"created_at"`
	LastUsed       time.Time     `json:"last_used"`
	TTLRemaining   time.Duration `json:"ttl_remaining"`
	WorkspaceFiles []SessionFile `json:"workspace_files"`
}

type CreatedSession struct {
	ID           string        `json:"session_id"`
	TTLRemaining time.Duration `json:"ttl_remaining"`
}

type BackendType string

const (
	BackendDocker BackendType = "docker"
	BackendGVisor BackendType = "gvisor"
)

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

var (
	_ Service = (*DockerBackend)(nil)
	_ Service = (*GVisorBackend)(nil)
)
