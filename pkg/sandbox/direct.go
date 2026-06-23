package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/config"
)

// directEnvPassthrough lists the non-sensitive process env vars the executed
// subprocess legitimately needs (locating python/subprocesses via PATH, text
// encoding, TLS roots). Everything else from the panda-server environment —
// notably PANDA_BOT_USERNAME / PANDA_BOT_TOKEN — is withheld: the executed code
// is LLM-generated and untrusted, and it reaches the data plane through req.Env
// (ETHPANDAOPS_API_URL + a scoped per-execution token), not the inherited env.
var directEnvPassthrough = []string{
	"PATH", "LANG", "LC_ALL", "LC_CTYPE", "TZ",
	"SSL_CERT_FILE", "SSL_CERT_DIR",
}

// DirectBackend implements sandbox execution by running Python directly as a
// subprocess on the host (no Docker containers). Intended for use inside a
// Kubernetes pod where the pod boundary itself provides the isolation.
type DirectBackend struct {
	cfg config.SandboxConfig
	log logrus.FieldLogger

	done chan struct{}
}

// NewDirectBackend creates a new direct execution backend.
func NewDirectBackend(cfg config.SandboxConfig, log logrus.FieldLogger) (*DirectBackend, error) {
	return &DirectBackend{
		cfg:  cfg,
		log:  log.WithField("component", "sandbox.direct"),
		done: make(chan struct{}),
	}, nil
}

// Name returns the backend name.
func (b *DirectBackend) Name() string {
	return "direct"
}

// Start validates that python3 is available on the host.
func (b *DirectBackend) Start(ctx context.Context) error {
	b.log.Info("Starting direct execution backend")

	// Verify python3 is available.
	if _, err := exec.LookPath("python3"); err != nil {
		// Try python as fallback.
		if _, err2 := exec.LookPath("python"); err2 != nil {
			return fmt.Errorf("python3 not found in PATH: %w", err)
		}
	}

	b.log.Info("Direct execution backend started")
	return nil
}

// Stop cleans up any resources. No-op for the direct backend.
func (b *DirectBackend) Stop(ctx context.Context) error {
	b.log.Info("Stopping direct execution backend")
	close(b.done)
	return nil
}

// Execute runs Python code directly as a subprocess.
func (b *DirectBackend) Execute(ctx context.Context, req ExecuteRequest) (*ExecutionResult, error) {
	executionID := req.ExecutionID
	if executionID == "" {
		executionID = uuid.New().String()
	}

	timeout := req.Timeout
	if timeout == 0 {
		timeout = time.Duration(b.cfg.Timeout) * time.Second
	}

	log := b.log.WithField("execution_id", executionID)
	log.Debug("Starting direct code execution")

	// Create a temporary directory for this execution.
	tmpDir, err := os.MkdirTemp("", fmt.Sprintf("panda-exec-%s-", executionID))
	if err != nil {
		return nil, fmt.Errorf("creating temp directory: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			log.WithError(err).Warn("Failed to cleanup temp directory")
		}
	}()

	// Write the script to a temp file.
	scriptPath := filepath.Join(tmpDir, "script.py")
	if err := os.WriteFile(scriptPath, []byte(req.Code), 0o644); err != nil {
		return nil, fmt.Errorf("writing script file: %w", err)
	}

	// Determine python binary.
	pythonBin := "python3"
	if _, err := exec.LookPath(pythonBin); err != nil {
		pythonBin = "python"
	}

	// Build the execution environment. Critically, do NOT inherit the
	// panda-server process env (os.Environ) — it holds the bot credential
	// (PANDA_BOT_*) and the executed code is untrusted. Mirror the docker
	// backend's isolation: sandbox defaults + a short non-sensitive passthrough +
	// the per-execution env panda built for the code (proxy URL + scoped token).
	envMap := SandboxEnvDefaults()
	for _, k := range directEnvPassthrough {
		if v, ok := os.LookupEnv(k); ok {
			envMap[k] = v
		}
	}
	for k, v := range req.Env {
		envMap[k] = v
	}
	envMap[EnvExecutionID] = executionID

	env := make([]string, 0, len(envMap))
	for k, v := range envMap {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}

	// Create execution context with timeout.
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	startTime := time.Now()

	// Build the command.
	cmd := exec.CommandContext(execCtx, pythonBin, scriptPath)
	cmd.Dir = tmpDir
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Run the command.
	err = cmd.Run()

	duration := time.Since(startTime).Seconds()

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else if strings.Contains(err.Error(), "signal: killed") || execCtx.Err() != nil {
			// Timeout or context cancellation.
			log.WithError(err).Warn("Execution timed out or cancelled")
			return nil, fmt.Errorf("execution timed out after %v: %w", timeout, execCtx.Err())
		} else {
			return nil, fmt.Errorf("execution failed: %w", err)
		}
	}

	log.WithFields(logrus.Fields{
		"exit_code": exitCode,
		"duration":  duration,
	}).Debug("Direct execution completed")

	return &ExecutionResult{
		Stdout:          stdout.String(),
		Stderr:          stderr.String(),
		ExitCode:        exitCode,
		ExecutionID:     executionID,
		DurationSeconds: duration,
	}, nil
}

// SessionsEnabled returns false — the direct backend doesn't support sessions.
func (b *DirectBackend) SessionsEnabled() bool {
	return false
}

// ListSessions returns an empty list — sessions not supported.
func (b *DirectBackend) ListSessions(_ context.Context, _ string) ([]SessionInfo, error) {
	return []SessionInfo{}, nil
}

// CreateSession returns an error — sessions not supported.
func (b *DirectBackend) CreateSession(_ context.Context, _ string, _ map[string]string) (string, error) {
	return "", fmt.Errorf("sessions not supported with direct backend")
}

// DestroySession returns an error — sessions not supported.
func (b *DirectBackend) DestroySession(_ context.Context, _, _ string) error {
	return fmt.Errorf("sessions not supported with direct backend")
}

// CanCreateSession returns false — sessions not supported.
func (b *DirectBackend) CanCreateSession(_ context.Context, _ string) (bool, int, int) {
	return false, 0, 0
}
