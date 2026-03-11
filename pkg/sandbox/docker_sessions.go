package sandbox

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"strconv"
	"time"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// executeWithNewSession creates a new session container and executes code in it.
func (b *DockerBackend) executeWithNewSession(ctx context.Context, req ExecuteRequest) (*ExecutionResult, error) {
	timeout := req.Timeout
	if timeout == 0 {
		timeout = time.Duration(b.cfg.Timeout) * time.Second
	}

	// Generate session ID upfront so it can be stored in container labels.
	sessionID := b.sessionManager.GenerateSessionID()

	log := b.log.WithFields(logrus.Fields{
		"mode":       "new-session",
		"session_id": sessionID,
	})
	log.Debug("Creating new session container")

	// Create the session container with session ID in labels.
	containerID, err := b.createSessionContainer(ctx, sessionID, req.Env, req.OwnerID)
	if err != nil {
		return nil, fmt.Errorf("creating session container: %w", err)
	}

	// Record initial access time for TTL tracking.
	b.sessionManager.RecordAccess(sessionID)

	log.Info("Created new session")

	// Build session object for execution.
	session := &Session{
		ID:          sessionID,
		OwnerID:     req.OwnerID,
		ContainerID: containerID,
		CreatedAt:   time.Now(),
		LastUsed:    time.Now(),
	}

	// Execute the code in the session.
	result, err := b.execInContainer(ctx, session, req.Code, timeout, req.Env)
	if err != nil {
		return nil, fmt.Errorf("executing in session: %w", err)
	}

	return b.prepareSessionResult(ctx, result, sessionID, containerID), nil
}

// executeInSession executes code in an existing session container.
func (b *DockerBackend) executeInSession(ctx context.Context, req ExecuteRequest) (*ExecutionResult, error) {
	timeout := req.Timeout
	if timeout == 0 {
		timeout = time.Duration(b.cfg.Timeout) * time.Second
	}

	log := b.log.WithFields(logrus.Fields{
		"mode":       "existing-session",
		"session_id": req.SessionID,
	})

	// Acquire the session for active use, which verifies ownership and refreshes access time.
	session, err := b.sessionManager.Acquire(ctx, req.SessionID, req.OwnerID)
	if err != nil {
		return nil, fmt.Errorf("getting session: %w", err)
	}

	log.Debug("Executing in existing session")

	// Execute the code in the session.
	result, err := b.execInContainer(ctx, session, req.Code, timeout, req.Env)
	if err != nil {
		return nil, fmt.Errorf("executing in session: %w", err)
	}

	return b.prepareSessionResult(ctx, result, session.ID, session.ContainerID), nil
}

// createSessionContainer creates a long-running container for session use.
// sessionID is stored in container labels for stateless session recovery.
func (b *DockerBackend) createSessionContainer(ctx context.Context, sessionID string, env map[string]string, ownerID string) (string, error) {
	// Merge environment variables with defaults.
	containerEnv := SandboxEnvDefaults()

	for k, v := range filterSessionEnv(env) {
		containerEnv[k] = v
	}

	// Convert map to slice for Docker API.
	envSlice := make([]string, 0, len(containerEnv))
	for k, v := range containerEnv {
		envSlice = append(envSlice, k+"="+v)
	}

	// Build labels for container identification and lifecycle management.
	// Session ID is stored in labels so sessions survive server restarts.
	labels := map[string]string{
		LabelManaged:   "true",
		LabelSessionID: sessionID,
		LabelCreatedAt: strconv.FormatInt(time.Now().Unix(), 10),
	}

	if ownerID != "" {
		labels[LabelOwnerID] = ownerID
	}

	// Session container runs sleep infinity and we exec into it.
	containerConfig := &container.Config{
		Image:      b.cfg.Image,
		Cmd:        []string{"sleep", "infinity"},
		Env:        envSlice,
		User:       "nobody",
		WorkingDir: "/workspace",
		Labels:     labels,
	}

	// Create workspace directory inside container.
	hostConfig := &container.HostConfig{
		NetworkMode: container.NetworkMode(b.cfg.Network),
		ExtraHosts:  []string{"host.docker.internal:host-gateway"},
	}

	// Apply security configuration.
	securityCfg, err := b.getSecurityConfig()
	if err != nil {
		return "", fmt.Errorf("getting security config: %w", err)
	}
	// For session containers, we need read-write root filesystem.
	securityCfg.ReadonlyRootfs = false
	securityCfg.ApplyToHostConfig(hostConfig)

	// Create container.
	resp, err := b.client.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, "")
	if err != nil {
		return "", fmt.Errorf("creating container: %w", err)
	}

	// Start container.
	if err := b.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		_ = b.forceRemoveContainer(ctx, resp.ID)
		return "", fmt.Errorf("starting container: %w", err)
	}

	// Create /workspace and /output directories inside the container.
	if err := b.createSessionDirs(ctx, resp.ID); err != nil {
		_ = b.forceRemoveContainer(ctx, resp.ID)
		return "", fmt.Errorf("creating session directories: %w", err)
	}

	return resp.ID, nil
}

func filterSessionEnv(env map[string]string) map[string]string {
	if env == nil {
		return nil
	}

	filtered := make(map[string]string, len(env))
	for k, v := range env {
		if k == "ETHPANDAOPS_API_TOKEN" {
			continue
		}
		filtered[k] = v
	}

	return filtered
}

// createSessionDirs creates the workspace and output directories inside a session container.
func (b *DockerBackend) createSessionDirs(ctx context.Context, containerID string) error {
	// Create directories and set permissions so nobody user can write.
	// We need to run as root to create dirs in /, then chmod for nobody.
	execConfig := container.ExecOptions{
		Cmd:          []string{"sh", "-c", "mkdir -p /workspace /output && chmod 777 /workspace /output"},
		AttachStdout: true,
		AttachStderr: true,
		User:         "root", // Run as root to create dirs and set permissions
	}

	execResp, err := b.client.ContainerExecCreate(ctx, containerID, execConfig)
	if err != nil {
		return fmt.Errorf("creating exec: %w", err)
	}

	if err := b.client.ContainerExecStart(ctx, execResp.ID, container.ExecStartOptions{}); err != nil {
		return fmt.Errorf("starting exec: %w", err)
	}

	return nil
}

// execInContainer executes Python code inside a running session container.
func (b *DockerBackend) execInContainer(
	ctx context.Context,
	session *Session,
	code string,
	timeout time.Duration,
	env map[string]string,
) (*ExecutionResult, error) {
	executionID := uuid.New().String()
	log := b.log.WithFields(logrus.Fields{
		"execution_id": executionID,
		"session_id":   session.ID,
		"container_id": session.ContainerID,
	})

	// Create execution context with timeout.
	execCtx, cancel := context.WithTimeout(ctx, timeout+5*time.Second)
	defer cancel()

	scriptPath := fmt.Sprintf("/tmp/script_%s.py", executionID)
	if err := b.writeSessionScript(execCtx, session.ContainerID, scriptPath, code); err != nil {
		return nil, err
	}

	startTime := time.Now()
	execResp, attachResp, err := b.startSessionExec(execCtx, session.ContainerID, scriptPath, executionID, env)
	if err != nil {
		return nil, err
	}
	defer attachResp.Close()

	stdout, stderr, err := b.readSessionExecOutput(execCtx, attachResp.Reader, session.ContainerID, scriptPath, timeout, log)
	if err != nil {
		return nil, err
	}

	inspectResp, err := b.client.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return nil, fmt.Errorf("inspecting exec: %w", err)
	}

	duration := time.Since(startTime).Seconds()
	b.cleanupSessionScript(ctx, session.ContainerID, scriptPath)

	log.WithFields(logrus.Fields{
		"exit_code": inspectResp.ExitCode,
		"duration":  duration,
	}).Debug("Session execution completed")

	return &ExecutionResult{
		Stdout:          stdout,
		Stderr:          stderr,
		ExitCode:        inspectResp.ExitCode,
		ExecutionID:     executionID,
		DurationSeconds: duration,
	}, nil
}

func (b *DockerBackend) writeSessionScript(
	ctx context.Context,
	containerID, scriptPath, code string,
) error {
	encoded := base64.StdEncoding.EncodeToString([]byte(code))
	writeCmd := []string{"sh", "-c", fmt.Sprintf("echo %s | base64 -d > %s", encoded, scriptPath)}

	writeResp, err := b.client.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          writeCmd,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return fmt.Errorf("creating write exec: %w", err)
	}

	if err := b.client.ContainerExecStart(ctx, writeResp.ID, container.ExecStartOptions{}); err != nil {
		return fmt.Errorf("starting write exec: %w", err)
	}

	return nil
}

func (b *DockerBackend) startSessionExec(
	ctx context.Context,
	containerID, scriptPath, executionID string,
	env map[string]string,
) (container.ExecCreateResponse, dockertypes.HijackedResponse, error) {
	execResp, err := b.client.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          []string{"python", scriptPath},
		AttachStdout: true,
		AttachStderr: true,
		Env:          buildSessionExecEnv(env, executionID),
	})
	if err != nil {
		return container.ExecCreateResponse{}, dockertypes.HijackedResponse{}, fmt.Errorf("creating exec: %w", err)
	}

	attachResp, err := b.client.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return container.ExecCreateResponse{}, dockertypes.HijackedResponse{}, fmt.Errorf("attaching to exec: %w", err)
	}

	return execResp, attachResp, nil
}

func buildSessionExecEnv(env map[string]string, executionID string) []string {
	execEnv := make([]string, 0, len(env)+1)
	for k, v := range env {
		if k == "ETHPANDAOPS_EXECUTION_ID" {
			continue
		}
		execEnv = append(execEnv, k+"="+v)
	}

	execEnv = append(execEnv, "ETHPANDAOPS_EXECUTION_ID="+executionID)

	return execEnv
}

func (b *DockerBackend) readSessionExecOutput(
	execCtx context.Context,
	reader io.Reader,
	containerID, scriptPath string,
	timeout time.Duration,
	log logrus.FieldLogger,
) (string, string, error) {
	var stdout, stderr bytes.Buffer
	done := make(chan error, 1)

	go func() {
		_, err := stdcopy.StdCopy(&stdout, &stderr, reader)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			log.WithError(err).Warn("Error reading exec output")
		}
	case <-execCtx.Done():
		log.Warn("Execution timed out, cleaning up script file")
		b.cleanupSessionScriptWithTimeout(containerID, scriptPath)
		return "", "", fmt.Errorf("execution timed out after %s", timeout)
	}

	return stdout.String(), stderr.String(), nil
}

func (b *DockerBackend) cleanupSessionScriptWithTimeout(containerID, scriptPath string) {
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cleanupCancel()
	b.cleanupSessionScript(cleanupCtx, containerID, scriptPath)
}

func (b *DockerBackend) cleanupSessionScript(ctx context.Context, containerID, scriptPath string) {
	cleanupResp, err := b.client.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd: []string{"rm", "-f", scriptPath},
	})
	if err != nil {
		return
	}

	_ = b.client.ContainerExecStart(ctx, cleanupResp.ID, container.ExecStartOptions{})
}

func (b *DockerBackend) prepareSessionResult(
	ctx context.Context,
	result *ExecutionResult,
	sessionID, containerID string,
) *ExecutionResult {
	result.SessionID = sessionID
	result.SessionTTLRemaining = b.sessionManager.TTLRemaining(sessionID)
	result.SessionFiles = b.collectSessionFiles(ctx, containerID)

	return result
}

// collectSessionFiles lists files in the session's /workspace directory.
func (b *DockerBackend) collectSessionFiles(ctx context.Context, containerID string) []SessionFile {
	execConfig := container.ExecOptions{
		Cmd:          []string{"find", "/workspace", "-maxdepth", "1", "-type", "f", "-printf", "%f\\t%s\\t%T@\\n"},
		AttachStdout: true,
		AttachStderr: true,
	}

	execResp, err := b.client.ContainerExecCreate(ctx, containerID, execConfig)
	if err != nil {
		b.log.WithError(err).Debug("Failed to create exec for listing session files")
		return nil
	}

	attachResp, err := b.client.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		b.log.WithError(err).Debug("Failed to attach to exec for listing session files")
		return nil
	}
	defer attachResp.Close()

	var stdout, stderr bytes.Buffer

	if _, err := stdcopy.StdCopy(&stdout, &stderr, attachResp.Reader); err != nil {
		b.log.WithError(err).Debug("Failed to read session files list")
		return nil
	}

	// Parse the output.
	files := make([]SessionFile, 0)
	lines := bytes.Split(bytes.TrimSpace(stdout.Bytes()), []byte("\n"))

	for _, line := range lines {
		if len(line) == 0 {
			continue
		}

		parts := bytes.Split(line, []byte("\t"))
		if len(parts) != 3 {
			continue
		}

		var size int64
		var modTime float64

		if _, err := fmt.Sscanf(string(parts[1]), "%d", &size); err != nil {
			continue
		}

		if _, err := fmt.Sscanf(string(parts[2]), "%f", &modTime); err != nil {
			continue
		}

		files = append(files, SessionFile{
			Name:     string(parts[0]),
			Size:     size,
			Modified: time.Unix(int64(modTime), 0),
		})
	}

	return files
}

// getSessionContainer queries Docker for a session container by session ID.
// Returns nil if not found.
func (b *DockerBackend) getSessionContainer(ctx context.Context, sessionID string) (*SessionContainer, error) {
	if b.client == nil {
		return nil, fmt.Errorf("docker client not initialized")
	}

	// Filter by session ID label.
	filterArgs := filters.NewArgs()
	filterArgs.Add("label", LabelManaged+"=true")
	filterArgs.Add("label", LabelSessionID+"="+sessionID)

	containers, err := b.client.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filterArgs,
	})
	if err != nil {
		return nil, fmt.Errorf("listing containers: %w", err)
	}

	if len(containers) == 0 {
		return nil, nil
	}

	c := containers[0]

	return &SessionContainer{
		ContainerID: c.ID,
		SessionID:   sessionID,
		OwnerID:     c.Labels[LabelOwnerID],
		CreatedAt:   parseContainerCreatedAt(c.Labels, c.Created),
	}, nil
}

// listAllSessionContainers queries Docker for all session containers.
func (b *DockerBackend) listAllSessionContainers(ctx context.Context) ([]*SessionContainer, error) {
	if b.client == nil {
		return nil, fmt.Errorf("docker client not initialized")
	}

	// Filter by managed label and session ID label (only session containers have session IDs).
	filterArgs := filters.NewArgs()
	filterArgs.Add("label", LabelManaged+"=true")
	filterArgs.Add("label", LabelSessionID)

	containers, err := b.client.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filterArgs,
	})
	if err != nil {
		return nil, fmt.Errorf("listing containers: %w", err)
	}

	result := make([]*SessionContainer, 0, len(containers))

	for _, c := range containers {
		sessionID := c.Labels[LabelSessionID]
		if sessionID == "" {
			continue
		}

		result = append(result, &SessionContainer{
			ContainerID: c.ID,
			SessionID:   sessionID,
			OwnerID:     c.Labels[LabelOwnerID],
			CreatedAt:   parseContainerCreatedAt(c.Labels, c.Created),
		})
	}

	return result, nil
}

// ListSessions returns all active sessions. If ownerID is non-empty, filters by owner.
func (b *DockerBackend) ListSessions(ctx context.Context, ownerID string) ([]SessionInfo, error) {
	if b.client == nil {
		return nil, fmt.Errorf("docker client not initialized")
	}

	if !b.sessionManager.Enabled() {
		return nil, fmt.Errorf("sessions are disabled")
	}

	containers, err := b.listAllSessionContainers(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing session containers: %w", err)
	}

	sessions := make([]SessionInfo, 0, len(containers))

	for _, c := range containers {
		// Filter by owner if specified.
		if ownerID != "" && c.OwnerID != "" && c.OwnerID != ownerID {
			continue
		}

		// Get last used time from session manager.
		lastUsed := b.sessionManager.GetLastUsed(c.SessionID)
		if lastUsed.IsZero() {
			// Session hasn't been accessed since server start, use created time.
			lastUsed = c.CreatedAt
		}

		// Collect workspace files.
		workspaceFiles := b.collectSessionFiles(ctx, c.ContainerID)

		sessions = append(sessions, SessionInfo{
			ID:             c.SessionID,
			CreatedAt:      c.CreatedAt,
			LastUsed:       lastUsed,
			TTLRemaining:   b.sessionManager.TTLRemaining(c.SessionID),
			WorkspaceFiles: workspaceFiles,
		})
	}

	return sessions, nil
}

// CreateSession creates a new empty session and returns its initial state.
func (b *DockerBackend) CreateSession(ctx context.Context, ownerID string, env map[string]string) (*CreatedSession, error) {
	if b.client == nil {
		return nil, fmt.Errorf("docker client not initialized")
	}

	if !b.sessionManager.Enabled() {
		return nil, fmt.Errorf("sessions are disabled")
	}

	// Check if we can create a new session.
	canCreate, count, maxAllowed := b.sessionManager.CanCreateSession(ctx, ownerID)
	if !canCreate {
		return nil, fmt.Errorf(
			"maximum sessions limit reached (%d/%d). Use manage_session with operation 'list' to see sessions, then 'destroy' to free up a slot",
			count, maxAllowed,
		)
	}

	// Generate session ID.
	sessionID := b.sessionManager.GenerateSessionID()

	log := b.log.WithFields(logrus.Fields{
		"session_id": sessionID,
		"owner_id":   ownerID,
	})
	log.Debug("Creating new session")

	// Create the session container.
	_, err := b.createSessionContainer(ctx, sessionID, env, ownerID)
	if err != nil {
		return nil, fmt.Errorf("creating session container: %w", err)
	}

	// Record initial access time for TTL tracking.
	b.sessionManager.RecordAccess(sessionID)

	log.Info("Created new session")

	return &CreatedSession{
		ID:           sessionID,
		TTLRemaining: b.sessionManager.TTLRemaining(sessionID),
	}, nil
}

// DestroySession destroys a session by ID.
// If ownerID is non-empty, verifies ownership before destroying.
func (b *DockerBackend) DestroySession(ctx context.Context, sessionID, ownerID string) error {
	if b.client == nil {
		return fmt.Errorf("docker client not initialized")
	}

	if !b.sessionManager.Enabled() {
		return fmt.Errorf("sessions are disabled")
	}

	return b.sessionManager.Destroy(ctx, sessionID, ownerID)
}

// CanCreateSession checks if a new session can be created.
// Returns (canCreate, currentCount, maxAllowed).
func (b *DockerBackend) CanCreateSession(ctx context.Context, ownerID string) (bool, int, int) {
	if !b.sessionManager.Enabled() {
		return false, 0, 0
	}

	return b.sessionManager.CanCreateSession(ctx, ownerID)
}

// SessionsEnabled returns whether sessions are enabled.
func (b *DockerBackend) SessionsEnabled() bool {
	return b.sessionManager.Enabled()
}
