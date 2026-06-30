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

// directSessionStore is the in-process SessionStore for the direct backend:
// sessions are workspace directories owned in a map, with the directory itself
// as the Session.Handle. Get/List hand out copies so SessionManager can annotate
// the returned Session without racing the map. All lifecycle policy lives in the
// SessionManager that drives this store.
type directSessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

func newDirectSessionStore() *directSessionStore {
	return &directSessionStore{sessions: make(map[string]*Session)}
}

func (s *directSessionStore) add(session *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.sessions[session.ID] = session
}

func (s *directSessionStore) Get(_ context.Context, sessionID string) (*Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	session, ok := s.sessions[sessionID]
	if !ok {
		return nil, nil
	}

	cp := *session

	return &cp, nil
}

func (s *directSessionStore) List(_ context.Context) ([]*Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*Session, 0, len(s.sessions))
	for _, session := range s.sessions {
		cp := *session
		out = append(out, &cp)
	}

	return out, nil
}

func (s *directSessionStore) Remove(_ context.Context, session *Session) error {
	s.mu.Lock()
	_, ok := s.sessions[session.ID]
	delete(s.sessions, session.ID)
	s.mu.Unlock()

	if !ok {
		return nil
	}

	return os.RemoveAll(session.Handle)
}

// DirectBackend implements sandbox execution by running Python directly as a
// subprocess on the host (no Docker containers). Intended for use inside a
// Kubernetes pod where the pod boundary itself provides the isolation.
//
// Session lifecycle (TTL, max-duration, ownership, the executing guard, limits,
// cleanup) is delegated to the shared SessionManager; this backend only owns the
// workspace directories (via directSessionStore) and the subprocess execution.
type DirectBackend struct {
	cfg config.SandboxConfig
	log logrus.FieldLogger

	store          *directSessionStore
	sessionManager *SessionManager
}

// NewDirectBackend creates a new direct execution backend.
func NewDirectBackend(cfg config.SandboxConfig, log logrus.FieldLogger) (*DirectBackend, error) {
	store := newDirectSessionStore()

	return &DirectBackend{
		cfg:            cfg,
		log:            log.WithField("component", "sandbox.direct"),
		store:          store,
		sessionManager: NewSessionManager(cfg.Sessions, log, store),
	}, nil
}

// Name returns the backend name.
func (b *DirectBackend) Name() string {
	return "direct"
}

// Start validates that a Python interpreter is available on the host and starts
// the session manager (which runs cleanup when sessions are enabled).
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

	if err := b.sessionManager.Start(ctx); err != nil {
		return err
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

// Stop shuts down the session manager, which reaps all session workspaces.
func (b *DirectBackend) Stop(ctx context.Context) error {
	b.log.Info("Stopping direct execution backend")

	return b.sessionManager.Stop(ctx)
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
	var (
		workDir string
		session *Session
	)

	if req.SessionID != "" {
		// Get verifies ownership, enforces TTL/max-duration, and refreshes LastUsed.
		s, err := b.sessionManager.Get(ctx, req.SessionID, req.OwnerID)
		if err != nil {
			return nil, err
		}

		session = s
		workDir = s.Handle

		// Protect the workspace from TTL purging while this execution runs.
		b.sessionManager.markExecuting(s.ID)
		defer b.sessionManager.unmarkExecuting(s.ID)
	} else {
		tmpDir, err := os.MkdirTemp("", fmt.Sprintf("panda-exec-%s-", executionID))
		if err != nil {
			return nil, fmt.Errorf("creating temp directory: %w", err)
		}

		workDir = tmpDir

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
		envMap[EnvSessionID] = session.ID
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
	err := cmd.Run()

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

	// Populate session info when running in a session. TTLRemaining tolerates a
	// session that was destroyed concurrently (returns the full TTL).
	if session != nil {
		result.SessionID = session.ID
		result.SessionTTLRemaining = b.sessionManager.TTLRemaining(session.ID)
		result.SessionFiles = b.listWorkspaceFiles(workDir)
	}

	return result, nil
}

// SessionsEnabled returns whether sessions are enabled in config.
func (b *DirectBackend) SessionsEnabled() bool {
	return b.sessionManager.Enabled()
}

// ListSessions returns all active sessions, optionally filtered by ownerID.
func (b *DirectBackend) ListSessions(ctx context.Context, ownerID string) ([]SessionInfo, error) {
	if !b.sessionManager.Enabled() {
		return nil, fmt.Errorf("sessions are disabled")
	}

	sessions, err := b.sessionManager.List(ctx)
	if err != nil {
		return nil, err
	}

	infos := make([]SessionInfo, 0, len(sessions))
	for _, s := range sessions {
		if ownerID != "" && s.OwnerID != "" && s.OwnerID != ownerID {
			continue
		}

		lastUsed := b.sessionManager.GetLastUsed(s.ID)
		if lastUsed.IsZero() {
			lastUsed = s.CreatedAt
		}

		infos = append(infos, SessionInfo{
			ID:             s.ID,
			CreatedAt:      s.CreatedAt,
			LastUsed:       lastUsed,
			TTLRemaining:   b.sessionManager.TTLRemaining(s.ID),
			WorkspaceFiles: b.listWorkspaceFiles(s.Handle),
		})
	}

	sort.Slice(infos, func(i, j int) bool {
		return infos[i].CreatedAt.Before(infos[j].CreatedAt)
	})

	return infos, nil
}

// CreateSession creates a new persistent workspace and returns its session ID.
func (b *DirectBackend) CreateSession(ctx context.Context, ownerID string, _ map[string]string) (string, error) {
	if !b.sessionManager.Enabled() {
		return "", fmt.Errorf("sessions are disabled")
	}

	canCreate, count, maxAllowed := b.sessionManager.CanCreateSession(ctx, ownerID)
	if !canCreate {
		return "", fmt.Errorf(
			"maximum sessions limit reached (%d/%d). Use manage_session with operation 'list' to see sessions, then 'destroy' to free up a slot",
			count, maxAllowed,
		)
	}

	sessionID := b.sessionManager.GenerateSessionID()

	workDir, err := os.MkdirTemp("", fmt.Sprintf("panda-session-%s-", sessionID))
	if err != nil {
		return "", fmt.Errorf("creating session workspace: %w", err)
	}

	b.store.add(&Session{
		ID:        sessionID,
		OwnerID:   ownerID,
		Handle:    workDir,
		CreatedAt: time.Now(),
	})
	b.sessionManager.RecordAccess(sessionID)

	b.log.WithField("session_id", sessionID).Info("Created session workspace")

	return sessionID, nil
}

// DestroySession removes a session's workspace and cleans up.
func (b *DirectBackend) DestroySession(ctx context.Context, sessionID, ownerID string) error {
	if !b.sessionManager.Enabled() {
		return fmt.Errorf("sessions are disabled")
	}

	return b.sessionManager.Destroy(ctx, sessionID, ownerID)
}

// CanCreateSession checks if a new session can be created within limits.
func (b *DirectBackend) CanCreateSession(ctx context.Context, ownerID string) (bool, int, int) {
	return b.sessionManager.CanCreateSession(ctx, ownerID)
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
