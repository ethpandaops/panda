package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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

// ensureWorkspaceRoot creates the dedicated workspace root off shared /tmp. 0711
// lets the exec uid traverse to its own workspace by exact path without being
// able to list siblings; each workspace under it is then locked to the server +
// exec uid (prepareWorkspace). Idempotent, so create paths can call it without
// depending on Start having run.
func (b *DirectBackend) ensureWorkspaceRoot() error {
	if err := os.MkdirAll(b.workspaceRoot, 0o711); err != nil {
		return fmt.Errorf("creating workspace root %s: %w", b.workspaceRoot, err)
	}

	// MkdirAll honors umask, so force the mode: a strict server umask must not
	// leave the root untraversable for the exec uid.
	if err := os.Chmod(b.workspaceRoot, 0o711); err != nil {
		return fmt.Errorf("setting workspace root mode: %w", err)
	}

	return nil
}

// prepareWorkspace locks a freshly created workspace to the server + exec uid,
// with no access for any other user on the host. Its group is set to the exec
// gid and its mode to setgid 0770: the exec uid (a member of that gid) reaches
// the workspace through the group, while others get nothing. The setgid bit
// makes files created inside inherit the exec gid, so the server can write the
// script group-readable for the exec uid without a per-file chown. Setting the
// group to a group the server is not a member of needs CAP_CHOWN, which the
// hardening preflight verifies — so this replaces the old world-writable 0777.
func (b *DirectBackend) prepareWorkspace(dir string) error {
	if err := os.Chown(dir, -1, b.cfg.ExecGID); err != nil {
		return fmt.Errorf("setting workspace group to exec gid %d: %w", b.cfg.ExecGID, err)
	}

	if err := os.Chmod(dir, os.ModeSetgid|0o770); err != nil {
		return fmt.Errorf("restricting workspace mode: %w", err)
	}

	return nil
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

	workspaceRoot  string
	store          *directSessionStore
	sessionManager *SessionManager
}

// NewDirectBackend creates a new direct execution backend.
func NewDirectBackend(cfg config.SandboxConfig, log logrus.FieldLogger) (*DirectBackend, error) {
	store := newDirectSessionStore()

	return &DirectBackend{
		cfg:            cfg,
		log:            log.WithField("component", "sandbox.direct"),
		workspaceRoot:  cfg.WorkspaceRoot(),
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

	// Fail closed: the direct backend only isolates untrusted code when the full
	// confinement stack (uid drop, namespaces, Landlock) is available. Verify it
	// before accepting any execution rather than degrading to unconfined.
	if err := preflightDirectHardening(b.cfg); err != nil {
		return fmt.Errorf("direct backend hardening preflight failed: %w", err)
	}

	// Hide the server's own /proc/<pid>/{environ,mem} from same-uid readers as
	// defense in depth for the /proc channel.
	if err := setServerNonDumpable(); err != nil {
		return fmt.Errorf("marking server non-dumpable: %w", err)
	}

	if err := b.ensureWorkspaceRoot(); err != nil {
		return err
	}

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
		if err := b.ensureWorkspaceRoot(); err != nil {
			return nil, err
		}

		tmpDir, err := os.MkdirTemp(b.workspaceRoot, fmt.Sprintf("panda-exec-%s-", executionID))
		if err != nil {
			return nil, fmt.Errorf("creating temp directory: %w", err)
		}

		if err := b.prepareWorkspace(tmpDir); err != nil {
			_ = os.RemoveAll(tmpDir)
			return nil, err
		}

		workDir = tmpDir

		defer func() {
			if err := os.RemoveAll(workDir); err != nil {
				log.WithError(err).Warn("Failed to cleanup temp directory")
			}
		}()
	}

	// Write the script group-readable (not world): the workspace's setgid bit
	// gives it the exec gid, so the sandbox uid reads it through the group while
	// other users on the host cannot.
	scriptPath := filepath.Join(workDir, "script.py")
	if err := os.WriteFile(scriptPath, []byte(req.Code), 0o640); err != nil {
		return nil, fmt.Errorf("writing script file: %w", err)
	}

	// Resolve to an absolute interpreter path: the trampoline execve()s it and
	// execve does not consult PATH.
	pythonBin, err := exec.LookPath(b.pythonBin())
	if err != nil {
		return nil, fmt.Errorf("resolving python interpreter %q: %w", b.pythonBin(), err)
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
	if session != nil {
		envMap[EnvSessionID] = session.ID
	}

	// Point every writable/home/cache path at the workspace so the Landlock
	// policy only has to grant write access to the single workspace directory.
	// These win over req.Env and SandboxEnvDefaults deliberately.
	for k, v := range map[string]string{
		"HOME": workDir, "TMPDIR": workDir, "TMP": workDir,
		"MPLCONFIGDIR": workDir, "XDG_CACHE_HOME": workDir,
	} {
		envMap[k] = v
	}

	env := make([]string, 0, len(envMap))
	for k, v := range envMap {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}

	// Create execution context with timeout.
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	startTime := time.Now()

	// Build the fully confined command: uid drop + mount/PID/net namespaces +
	// Landlock, applied by the re-exec trampoline before it execs Python.
	cmd, cleanupCmd, err := newHardenedSandboxCmd(execCtx, workDir, scriptPath, pythonBin, b.cfg.ExecUID, b.cfg.ExecGID, env)
	if err != nil {
		return nil, fmt.Errorf("building sandbox command: %w", err)
	}
	defer cleanupCmd()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Run the command.
	err = cmd.Run()

	duration := time.Since(startTime).Seconds()

	exitCode := 0
	if err != nil {
		// CommandContext SIGKILLs on expiry, which surfaces as *exec.ExitError.
		// The context must therefore be consulted before the exit code is
		// trusted, or a killed execution reports itself as a clean run.
		switch {
		case errors.Is(execCtx.Err(), context.DeadlineExceeded):
			log.WithError(err).Warn("Execution timed out")

			return nil, fmt.Errorf("execution timed out after %v", timeout)
		case execCtx.Err() != nil:
			log.WithError(err).Warn("Execution cancelled")

			return nil, fmt.Errorf("execution cancelled: %w", execCtx.Err())
		}

		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return nil, fmt.Errorf("execution failed: %w", err)
		}

		exitCode = exitErr.ExitCode()
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

	if err := b.ensureWorkspaceRoot(); err != nil {
		return "", err
	}

	workDir, err := os.MkdirTemp(b.workspaceRoot, fmt.Sprintf("panda-session-%s-", sessionID))
	if err != nil {
		return "", fmt.Errorf("creating session workspace: %w", err)
	}

	if err := b.prepareWorkspace(workDir); err != nil {
		_ = os.RemoveAll(workDir)
		return "", err
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
