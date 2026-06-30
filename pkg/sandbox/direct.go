package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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

// directSession represents a persistent workspace directory for a session.
type directSession struct {
	id        string
	ownerID   string
	workDir   string
	createdAt time.Time
	lastUsed  time.Time
	// executing counts in-flight executions in this session. While > 0 the
	// idle/max-duration cleanup must not remove the workspace out from under
	// the running subprocess (mirrors the docker backend's markExecuting).
	executing int
}

// DirectBackend implements sandbox execution by running Python directly as a
// subprocess on the host (no Docker containers). Intended for use inside a
// Kubernetes pod where the pod boundary itself provides the isolation.
type DirectBackend struct {
	cfg config.SandboxConfig
	log logrus.FieldLogger

	mu       sync.RWMutex
	sessions map[string]*directSession
	done     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// NewDirectBackend creates a new direct execution backend.
func NewDirectBackend(cfg config.SandboxConfig, log logrus.FieldLogger) (*DirectBackend, error) {
	return &DirectBackend{
		cfg:      cfg,
		log:      log.WithField("component", "sandbox.direct"),
		sessions: make(map[string]*directSession),
		done:     make(chan struct{}),
	}, nil
}

// Name returns the backend name.
func (b *DirectBackend) Name() string {
	return "direct"
}

// Start validates that python3 is available on the host and kicks off
// session cleanup when sessions are enabled.
func (b *DirectBackend) Start(ctx context.Context) error {
	b.log.Info("Starting direct execution backend")

	// Verify the Python interpreter is available. A configured python_path must
	// exist — fail fast rather than silently falling back to an ambient python
	// that may lack the sandbox's dependency-complete environment.
	if b.cfg.PythonPath != "" {
		if _, err := exec.LookPath(b.cfg.PythonPath); err != nil {
			return fmt.Errorf("configured sandbox.python_path %q not executable: %w", b.cfg.PythonPath, err)
		}
	} else if _, err := exec.LookPath("python3"); err != nil {
		if _, err2 := exec.LookPath("python"); err2 != nil {
			return fmt.Errorf("python3 not found in PATH: %w", err)
		}
	}

	if b.cfg.Sessions.IsEnabled() {
		b.wg.Add(1)
		go b.cleanupLoop()
	}

	b.log.WithField("python", b.pythonBin()).Info("Direct execution backend started")
	return nil
}

// pythonBin returns the Python interpreter to invoke: the configured path when
// set, otherwise python3 with a python fallback.
func (b *DirectBackend) pythonBin() string {
	if b.cfg.PythonPath != "" {
		return b.cfg.PythonPath
	}
	if _, err := exec.LookPath("python3"); err == nil {
		return "python3"
	}
	return "python"
}

// Stop cleans up all session workspaces and shuts down.
func (b *DirectBackend) Stop(ctx context.Context) error {
	b.log.Info("Stopping direct execution backend")
	b.stopOnce.Do(func() { close(b.done) })
	b.wg.Wait()

	// Clean up all session workspaces.
	b.mu.Lock()
	defer b.mu.Unlock()

	for id, s := range b.sessions {
		if err := os.RemoveAll(s.workDir); err != nil {
			b.log.WithField("session_id", id).WithError(err).Warn("Failed to cleanup session workspace")
		}
	}
	b.sessions = make(map[string]*directSession)

	return nil
}

// Execute runs Python code directly as a subprocess. When req.SessionID is set
// and non-empty the subprocess runs in the session's persistent workspace, and
// the result carries session info.
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

	// Resolve the working directory: session workspace or a fresh temp dir.
	workDir, session, err := b.resolveWorkDir(req.SessionID, req.OwnerID, executionID)
	if err != nil {
		return nil, err
	}
	if session != nil {
		// Release the in-flight guard once this execution completes so cleanup
		// can reclaim the session.
		defer b.finishExecuting(session.id)
	} else if workDir != "" {
		defer func() {
			if err := os.RemoveAll(workDir); err != nil {
				log.WithError(err).Warn("Failed to cleanup temp directory")
			}
		}()
	}

	// Write the script to a temp file.
	scriptPath := filepath.Join(workDir, "script.py")
	if err := os.WriteFile(scriptPath, []byte(req.Code), 0o644); err != nil {
		return nil, fmt.Errorf("writing script file: %w", err)
	}

	pythonBin := b.pythonBin()

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
	if session != nil {
		envMap[EnvSessionID] = session.id
	}

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
	cmd.Dir = workDir
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

	result := &ExecutionResult{
		Stdout:          stdout.String(),
		Stderr:          stderr.String(),
		ExitCode:        exitCode,
		ExecutionID:     executionID,
		DurationSeconds: duration,
	}

	// Populate session info when running in a session. Read lastUsed from the
	// captured session pointer (still valid even if the session was destroyed
	// concurrently) rather than re-indexing the map, which would nil-deref.
	if session != nil {
		b.mu.RLock()
		lastUsed := session.lastUsed
		b.mu.RUnlock()

		result.SessionID = session.id
		result.SessionTTLRemaining = b.ttlRemaining(session.id, lastUsed)
		result.SessionFiles = b.listWorkspaceFiles(workDir)
	}

	return result, nil
}

// resolveWorkDir returns the working directory for an execution and, when
// running inside a session, the session itself with its in-flight counter
// incremented — callers MUST pair a non-nil session with finishExecuting.
// Non-session executions get a temp dir that the caller must clean up.
func (b *DirectBackend) resolveWorkDir(sessionID, ownerID, executionID string) (string, *directSession, error) {
	if sessionID != "" {
		b.mu.Lock()
		defer b.mu.Unlock()

		s, ok := b.sessions[sessionID]
		if !ok {
			return "", nil, fmt.Errorf("session %s not found", sessionID)
		}

		// Enforce ownership: a caller may only execute in its own session.
		// Mirrors DestroySession; the docker backend enforces this via
		// sessionManager.Get(ctx, sessionID, ownerID).
		if s.ownerID != "" && ownerID != "" && s.ownerID != ownerID {
			return "", nil, fmt.Errorf("session %s not owned by caller", sessionID)
		}

		s.lastUsed = time.Now()
		s.executing++

		return s.workDir, s, nil
	}

	// No session — create a fresh temp dir.
	prefix := executionID
	if prefix == "" {
		prefix = uuid.New().String()
	}
	tmpDir, err := os.MkdirTemp("", fmt.Sprintf("panda-exec-%s-", prefix))
	if err != nil {
		return "", nil, fmt.Errorf("creating temp directory: %w", err)
	}

	return tmpDir, nil, nil
}

// finishExecuting releases the in-flight guard taken by resolveWorkDir so the
// cleanup loop may reclaim the session once no executions remain.
func (b *DirectBackend) finishExecuting(sessionID string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if s, ok := b.sessions[sessionID]; ok && s.executing > 0 {
		s.executing--
	}
}

// ttlRemaining returns the time until the session expires from inactivity.
// Returns the full configured TTL when lastUsed is zero (e.g. brand-new session).
func (b *DirectBackend) ttlRemaining(sessionID string, lastUsed time.Time) time.Duration {
	if lastUsed.IsZero() {
		return b.cfg.Sessions.TTL
	}

	remaining := b.cfg.Sessions.TTL - time.Since(lastUsed)
	if remaining < 0 {
		return 0
	}

	return remaining
}

// SessionsEnabled returns whether sessions are enabled in config.
func (b *DirectBackend) SessionsEnabled() bool {
	return b.cfg.Sessions.IsEnabled()
}

// ListSessions returns all active sessions, optionally filtered by ownerID.
func (b *DirectBackend) ListSessions(_ context.Context, ownerID string) ([]SessionInfo, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	infos := make([]SessionInfo, 0, len(b.sessions))
	for _, s := range b.sessions {
		if ownerID != "" && s.ownerID != ownerID {
			continue
		}

		infos = append(infos, SessionInfo{
			ID:           s.id,
			CreatedAt:    s.createdAt,
			LastUsed:     s.lastUsed,
			TTLRemaining: b.ttlRemaining(s.id, s.lastUsed),
		})
	}

	sort.Slice(infos, func(i, j int) bool {
		return infos[i].CreatedAt.Before(infos[j].CreatedAt)
	})

	return infos, nil
}

// CreateSession creates a new persistent workspace and returns its session ID.
func (b *DirectBackend) CreateSession(_ context.Context, ownerID string, _ map[string]string) (string, error) {
	if !b.cfg.Sessions.IsEnabled() {
		return "", fmt.Errorf("sessions are disabled")
	}

	sessionID := strings.ReplaceAll(uuid.New().String(), "-", "")[:12]

	workDir, err := os.MkdirTemp("", fmt.Sprintf("panda-session-%s-", sessionID))
	if err != nil {
		return "", fmt.Errorf("creating session workspace: %w", err)
	}

	now := time.Now()

	b.mu.Lock()
	b.sessions[sessionID] = &directSession{
		id:        sessionID,
		ownerID:   ownerID,
		workDir:   workDir,
		createdAt: now,
		lastUsed:  now,
	}
	b.mu.Unlock()

	b.log.WithField("session_id", sessionID).Info("Created session workspace")

	return sessionID, nil
}

// DestroySession removes a session's workspace and cleans up.
func (b *DirectBackend) DestroySession(_ context.Context, sessionID, ownerID string) error {
	b.mu.RLock()
	s, ok := b.sessions[sessionID]
	b.mu.RUnlock()

	if !ok {
		return fmt.Errorf("session %s not found", sessionID)
	}

	if ownerID != "" && s.ownerID != "" && s.ownerID != ownerID {
		return fmt.Errorf("session %s not owned by caller", sessionID)
	}

	b.mu.Lock()
	delete(b.sessions, sessionID)
	b.mu.Unlock()

	if err := os.RemoveAll(s.workDir); err != nil {
		b.log.WithField("session_id", sessionID).WithError(err).Warn("Failed to cleanup session workspace")
	}

	b.log.WithField("session_id", sessionID).Info("Destroyed session workspace")

	return nil
}

// CanCreateSession checks if a new session can be created within limits.
func (b *DirectBackend) CanCreateSession(_ context.Context, ownerID string) (bool, int, int) {
	maxSessions := b.cfg.Sessions.MaxSessions
	if maxSessions <= 0 {
		return true, 0, 0
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	count := 0
	for _, s := range b.sessions {
		if ownerID == "" || s.ownerID == ownerID {
			count++
		}
	}

	return count < maxSessions, count, maxSessions
}

// cleanupLoop runs periodically to destroy expired sessions.
func (b *DirectBackend) cleanupLoop() {
	defer b.wg.Done()

	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-b.done:
			return
		case <-ticker.C:
			b.cleanupExpired()
		}
	}
}

// cleanupExpired destroys sessions that have exceeded TTL or MaxDuration.
func (b *DirectBackend) cleanupExpired() {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	for id, s := range b.sessions {
		// Never reclaim a session with an execution in flight — removing its
		// workspace would pull files out from under the running subprocess.
		if s.executing > 0 {
			continue
		}

		// Check max duration.
		if now.Sub(s.createdAt) > b.cfg.Sessions.MaxDuration {
			b.log.WithField("session_id", id).Info("Session expired (max duration)")
			if err := os.RemoveAll(s.workDir); err != nil {
				b.log.WithField("session_id", id).WithError(err).Warn("Failed to cleanup session workspace")
			}
			delete(b.sessions, id)
			continue
		}

		// Check TTL (idle timeout).
		if now.Sub(s.lastUsed) > b.cfg.Sessions.TTL {
			b.log.WithField("session_id", id).Info("Session expired (idle TTL)")
			if err := os.RemoveAll(s.workDir); err != nil {
				b.log.WithField("session_id", id).WithError(err).Warn("Failed to cleanup session workspace")
			}
			delete(b.sessions, id)
		}
	}
}

// listWorkspaceFiles returns the files in the workspace directory for session
// reporting, excluding the script.py file created for each execution.
func (b *DirectBackend) listWorkspaceFiles(workDir string) []SessionFile {
	entries, err := os.ReadDir(workDir)
	if err != nil {
		return nil
	}

	var files []SessionFile
	for _, e := range entries {
		if e.IsDir() || e.Name() == "script.py" {
			continue
		}

		info, err := e.Info()
		if err != nil {
			continue
		}

		files = append(files, SessionFile{
			Name:     e.Name(),
			Size:     info.Size(),
			Modified: info.ModTime(),
		})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Name < files[j].Name
	})

	return files
}
