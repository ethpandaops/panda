package sandbox

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/containerd/errdefs"
	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/config"
)

// Container label keys for identifying and managing ethpandaops-panda containers.
const (
	// LabelManaged identifies containers created by ethpandaops-panda.
	LabelManaged = "io.ethpandaops-panda.managed"
	// LabelCreatedAt stores the Unix timestamp when the container was created.
	LabelCreatedAt = "io.ethpandaops-panda.created-at"
	// LabelSessionID stores the session ID for session containers.
	LabelSessionID = "io.ethpandaops-panda.session-id"
	// LabelOwnerID stores the owner ID (GitHub user ID) if auth is enabled.
	LabelOwnerID = "io.ethpandaops-panda.owner-id"
)

// parseContainerCreatedAt extracts the creation time from container labels.
// Falls back to Docker's created timestamp if the label is missing or invalid.
func parseContainerCreatedAt(labels map[string]string, dockerCreated int64) time.Time {
	if createdAtStr, ok := labels[LabelCreatedAt]; ok {
		if createdAtUnix, err := strconv.ParseInt(createdAtStr, 10, 64); err == nil {
			return time.Unix(createdAtUnix, 0)
		}
	}

	return time.Unix(dockerCreated, 0)
}

// SecurityConfigFunc is a function that returns security configuration.
type SecurityConfigFunc func(memoryLimit string, cpuLimit float64) (*SecurityConfig, error)

// DockerBackend implements sandbox execution using standard Docker containers.
type DockerBackend struct {
	cfg    config.SandboxConfig
	log    logrus.FieldLogger
	client *client.Client

	// activeContainers tracks running containers for cleanup on timeout/shutdown.
	activeContainers map[string]string // executionID -> containerID
	mu               sync.RWMutex

	// sessionManager handles persistent session lifecycle.
	sessionManager *SessionManager

	// securityConfigFunc returns the security configuration.
	// This allows gVisor backend to override with gVisor-specific config.
	securityConfigFunc SecurityConfigFunc
}

// NewDockerBackend creates a new Docker sandbox backend.
func NewDockerBackend(cfg config.SandboxConfig, log logrus.FieldLogger) (*DockerBackend, error) {
	backend := &DockerBackend{
		cfg:                cfg,
		log:                log.WithField("component", "sandbox.docker"),
		activeContainers:   make(map[string]string, 16),
		securityConfigFunc: DefaultSecurityConfig,
	}

	// Create session manager with callbacks for container queries and cleanup.
	backend.sessionManager = NewSessionManager(
		cfg.Sessions,
		log,
		backend.getSessionContainer,
		backend.listAllSessionContainers,
		func(ctx context.Context, containerID string) error {
			if backend.client == nil {
				return nil
			}
			return backend.forceRemoveContainer(ctx, containerID)
		},
	)

	return backend, nil
}

// Name returns the backend name.
func (b *DockerBackend) Name() string {
	return "docker"
}

// Start initializes the Docker client and verifies connectivity.
func (b *DockerBackend) Start(ctx context.Context) error {
	b.log.Info("Starting Docker sandbox backend")

	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("creating docker client: %w", err)
	}

	// Verify Docker is accessible.
	if _, err := dockerClient.Ping(ctx); err != nil {
		return fmt.Errorf("connecting to docker daemon: %w", err)
	}

	b.client = dockerClient

	// Clean up expired orphaned containers from previous runs.
	// Only removes containers older than max session duration to avoid
	// disrupting active sessions from other server instances.
	if err := b.cleanupExpiredContainers(ctx); err != nil {
		b.log.WithError(err).Warn("Failed to cleanup expired containers")
	}

	// Ensure the sandbox image is available.
	if err := b.ensureImage(ctx); err != nil {
		return fmt.Errorf("ensuring sandbox image: %w", err)
	}

	// Ensure the configured network exists (auto-creates if missing).
	if err := b.ensureNetwork(ctx); err != nil {
		return fmt.Errorf("ensuring sandbox network: %w", err)
	}

	// Start session manager if enabled.
	if err := b.sessionManager.Start(ctx); err != nil {
		return fmt.Errorf("starting session manager: %w", err)
	}

	b.log.WithField("image", b.cfg.Image).Info("Docker sandbox backend started")

	return nil
}

// Stop cleans up any active containers and closes the Docker client.
func (b *DockerBackend) Stop(ctx context.Context) error {
	b.log.Info("Stopping Docker sandbox backend")

	// Stop session manager first (this will cleanup session containers).
	if err := b.sessionManager.Stop(ctx); err != nil {
		b.log.WithError(err).Warn("Failed to stop session manager")
	}

	// Kill all active containers.
	b.mu.Lock()
	containersToClean := make(map[string]string, len(b.activeContainers))

	for k, v := range b.activeContainers {
		containersToClean[k] = v
	}

	b.activeContainers = make(map[string]string, 16)
	b.mu.Unlock()

	for execID, containerID := range containersToClean {
		if err := b.forceRemoveContainer(ctx, containerID); err != nil {
			b.log.WithFields(logrus.Fields{
				"execution_id": execID,
				"container_id": containerID,
				"error":        err,
			}).Warn("Failed to remove container during shutdown")
		}
	}

	if b.client != nil {
		if err := b.client.Close(); err != nil {
			return fmt.Errorf("closing docker client: %w", err)
		}
	}

	b.log.Info("Docker sandbox backend stopped")

	return nil
}

// Execute runs Python code in a Docker container.
func (b *DockerBackend) Execute(ctx context.Context, req ExecuteRequest) (*ExecutionResult, error) {
	if b.client == nil {
		return nil, fmt.Errorf("docker client not initialized, call Start() first")
	}

	// If a session ID is provided, execute in the existing session.
	if req.SessionID != "" {
		return b.executeInSession(ctx, req)
	}

	// If sessions are enabled, create a new session.
	if b.sessionManager.Enabled() {
		return b.executeWithNewSession(ctx, req)
	}

	// Ephemeral execution (original behavior).
	return b.executeEphemeral(ctx, req)
}

// executeEphemeral runs code in a new container that is destroyed after execution.
func (b *DockerBackend) executeEphemeral(ctx context.Context, req ExecuteRequest) (*ExecutionResult, error) {
	executionID := uuid.New().String()
	timeout := req.Timeout

	if timeout == 0 {
		timeout = time.Duration(b.cfg.Timeout) * time.Second
	}

	log := b.log.WithField("execution_id", executionID)
	log.Debug("Starting ephemeral code execution")

	// Create temporary directories for this execution.
	baseDir, err := b.createExecutionDirs(executionID)
	if err != nil {
		return nil, fmt.Errorf("creating execution directories: %w", err)
	}

	defer func() {
		if err := os.RemoveAll(baseDir); err != nil {
			log.WithError(err).Warn("Failed to cleanup execution directory")
		}
	}()

	sharedDir := filepath.Join(baseDir, "shared")
	outputDir := filepath.Join(baseDir, "output")

	// Write the script to execute.
	scriptPath := filepath.Join(sharedDir, "script.py")
	if err := os.WriteFile(scriptPath, []byte(req.Code), 0644); err != nil {
		return nil, fmt.Errorf("writing script file: %w", err)
	}

	// Inject execution ID into environment for storage.upload() to use.
	env := req.Env
	if env == nil {
		env = make(map[string]string)
	}

	env["ETHPANDAOPS_EXECUTION_ID"] = executionID

	// Build container configuration.
	containerConfig, hostConfig, err := b.buildContainerConfig(sharedDir, outputDir, env)
	if err != nil {
		return nil, fmt.Errorf("building container config: %w", err)
	}

	// Create execution context with timeout.
	execCtx, cancel := context.WithTimeout(ctx, timeout+5*time.Second)
	defer cancel()

	// Create container.
	resp, err := b.client.ContainerCreate(execCtx, containerConfig, hostConfig, nil, nil, "")
	if err != nil {
		return nil, fmt.Errorf("creating container: %w", err)
	}

	containerID := resp.ID
	b.trackContainer(executionID, containerID)

	defer func() {
		b.untrackContainer(executionID)

		if err := b.forceRemoveContainer(context.Background(), containerID); err != nil {
			log.WithError(err).Warn("Failed to remove container")
		}
	}()

	// Start container.
	startTime := time.Now()

	if err := b.client.ContainerStart(execCtx, containerID, container.StartOptions{}); err != nil {
		return nil, fmt.Errorf("starting container: %w", err)
	}

	log.Debug("Container started")

	// Wait for container to finish or timeout.
	result, err := b.waitForContainer(execCtx, containerID, timeout)
	if err != nil {
		// On timeout, force kill the container.
		log.Warn("Container execution timed out, force killing")

		if killErr := b.forceKillContainer(context.Background(), containerID); killErr != nil {
			log.WithError(killErr).Warn("Failed to force kill container")
		}

		return nil, fmt.Errorf("container execution: %w", err)
	}

	duration := time.Since(startTime).Seconds()

	// Collect output files.
	outputFiles, err := b.collectOutputFiles(outputDir)
	if err != nil {
		log.WithError(err).Warn("Failed to collect output files")
	}

	// Read metrics if present.
	metrics := b.readMetrics(outputDir)

	log.WithFields(logrus.Fields{
		"exit_code": result.exitCode,
		"duration":  duration,
	}).Debug("Container execution completed")

	return &ExecutionResult{
		Stdout:          result.stdout,
		Stderr:          result.stderr,
		ExitCode:        result.exitCode,
		ExecutionID:     executionID,
		OutputFiles:     outputFiles,
		Metrics:         metrics,
		DurationSeconds: duration,
	}, nil
}

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

// containerResult holds the output from container execution.
type containerResult struct {
	stdout   string
	stderr   string
	exitCode int
}

// createExecutionDirs creates the temporary directories for an execution.
func (b *DockerBackend) createExecutionDirs(executionID string) (string, error) {
	var baseDir string

	if b.cfg.HostSharedPath != "" {
		// Docker-in-Docker mode: use host-visible path.
		baseDir = filepath.Join(b.cfg.HostSharedPath, executionID)
	} else {
		// Direct mode: use temp directory.
		baseDir = filepath.Join(os.TempDir(), "ethpandaops-panda-sandbox-"+executionID)
	}

	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return "", fmt.Errorf("creating base directory: %w", err)
	}

	sharedDir := filepath.Join(baseDir, "shared")
	outputDir := filepath.Join(baseDir, "output")

	if err := os.MkdirAll(sharedDir, 0755); err != nil {
		return "", fmt.Errorf("creating shared directory: %w", err)
	}

	// Output dir needs 777 permissions for "nobody" user to write.
	if err := os.MkdirAll(outputDir, 0777); err != nil {
		return "", fmt.Errorf("creating output directory: %w", err)
	}

	return baseDir, nil
}

// buildContainerConfig creates the container and host configurations.
func (b *DockerBackend) buildContainerConfig(
	sharedDir, outputDir string,
	env map[string]string,
) (*container.Config, *container.HostConfig, error) {
	// Merge environment variables with defaults.
	containerEnv := SandboxEnvDefaults()
	for k, v := range env {
		containerEnv[k] = v
	}

	// Convert map to slice for Docker API.
	envSlice := make([]string, 0, len(containerEnv))
	for k, v := range containerEnv {
		envSlice = append(envSlice, k+"="+v)
	}

	containerConfig := &container.Config{
		Image: b.cfg.Image,
		Cmd:   []string{"python", "/shared/script.py"},
		Env:   envSlice,
		User:  "nobody",
		Labels: map[string]string{
			LabelManaged:   "true",
			LabelCreatedAt: strconv.FormatInt(time.Now().Unix(), 10),
		},
	}

	// Determine the source paths for mounts.
	// In Docker-in-Docker mode, HostSharedPath is the path visible to the Docker daemon.
	var hostSharedDir, hostOutputDir string

	if b.cfg.HostSharedPath != "" {
		// Extract the execution ID from the directory path.
		execID := filepath.Base(filepath.Dir(sharedDir))
		hostSharedDir = filepath.Join(b.cfg.HostSharedPath, execID, "shared")
		hostOutputDir = filepath.Join(b.cfg.HostSharedPath, execID, "output")
	} else {
		hostSharedDir = sharedDir
		hostOutputDir = outputDir
	}

	hostConfig := &container.HostConfig{
		NetworkMode: container.NetworkMode(b.cfg.Network),
		Mounts:      CreateMounts(hostSharedDir, hostOutputDir),
		ExtraHosts:  []string{"host.docker.internal:host-gateway"},
	}

	// Apply security configuration.
	securityCfg, err := b.getSecurityConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("getting security config: %w", err)
	}

	securityCfg.ApplyToHostConfig(hostConfig)

	return containerConfig, hostConfig, nil
}

// getSecurityConfig returns the security configuration for this backend.
func (b *DockerBackend) getSecurityConfig() (*SecurityConfig, error) {
	return b.securityConfigFunc(b.cfg.MemoryLimit, b.cfg.CPULimit)
}

// waitForContainer waits for a container to finish and returns its output.
func (b *DockerBackend) waitForContainer(
	ctx context.Context,
	containerID string,
	timeout time.Duration,
) (*containerResult, error) {
	// Create a timeout context for waiting.
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Wait for container to exit.
	statusCh, errCh := b.client.ContainerWait(waitCtx, containerID, container.WaitConditionNotRunning)

	select {
	case err := <-errCh:
		if err != nil {
			return nil, fmt.Errorf("waiting for container: %w", err)
		}
	case status := <-statusCh:
		// Container exited, get logs.
		stdout, stderr, err := b.getContainerLogs(ctx, containerID)
		if err != nil {
			return nil, fmt.Errorf("getting container logs: %w", err)
		}

		return &containerResult{
			stdout:   stdout,
			stderr:   stderr,
			exitCode: int(status.StatusCode),
		}, nil
	case <-waitCtx.Done():
		return nil, fmt.Errorf("execution timed out after %s", timeout)
	}

	return nil, fmt.Errorf("unexpected wait state")
}

// getContainerLogs retrieves stdout and stderr from a container.
func (b *DockerBackend) getContainerLogs(ctx context.Context, containerID string) (string, string, error) {
	logReader, err := b.client.ContainerLogs(ctx, containerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
	})
	if err != nil {
		return "", "", fmt.Errorf("reading container logs: %w", err)
	}
	defer func() { _ = logReader.Close() }()

	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, logReader); err != nil {
		return "", "", fmt.Errorf("demultiplexing container logs: %w", err)
	}

	return stdout.String(), stderr.String(), nil
}

// collectOutputFiles lists files in the output directory.
func (b *DockerBackend) collectOutputFiles(outputDir string) ([]string, error) {
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return nil, fmt.Errorf("reading output directory: %w", err)
	}

	files := make([]string, 0, len(entries))

	for _, entry := range entries {
		if !entry.IsDir() && entry.Name()[0] != '.' {
			files = append(files, entry.Name())
		}
	}

	return files, nil
}

// readMetrics reads the metrics file if present.
func (b *DockerBackend) readMetrics(outputDir string) map[string]any {
	metricsPath := filepath.Join(outputDir, ".metrics.json")

	data, err := os.ReadFile(metricsPath)
	if err != nil {
		return nil
	}

	var metrics map[string]any
	if err := json.Unmarshal(data, &metrics); err != nil {
		b.log.WithError(err).Warn("Failed to parse metrics file")

		return nil
	}

	return metrics
}

// trackContainer adds a container to the active containers map.
func (b *DockerBackend) trackContainer(executionID, containerID string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.activeContainers[executionID] = containerID
}

// untrackContainer removes a container from the active containers map.
func (b *DockerBackend) untrackContainer(executionID string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	delete(b.activeContainers, executionID)
}

// forceKillContainer forcefully kills a running container.
func (b *DockerBackend) forceKillContainer(ctx context.Context, containerID string) error {
	if err := b.client.ContainerKill(ctx, containerID, "SIGKILL"); err != nil {
		// Ignore "not found" errors.
		if !errdefs.IsNotFound(err) {
			return fmt.Errorf("killing container: %w", err)
		}
	}

	return nil
}

// forceRemoveContainer forcefully removes a container.
func (b *DockerBackend) forceRemoveContainer(ctx context.Context, containerID string) error {
	if err := b.client.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true}); err != nil {
		if !errdefs.IsNotFound(err) {
			return fmt.Errorf("removing container: %w", err)
		}
	}

	return nil
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

// cleanupExpiredContainers removes ethpandaops-panda containers that have exceeded max session duration.
// This handles orphaned containers from previous server instances that were killed abruptly.
func (b *DockerBackend) cleanupExpiredContainers(ctx context.Context) error {
	// Find all containers with our managed label.
	filterArgs := filters.NewArgs()
	filterArgs.Add("label", LabelManaged+"=true")

	containers, err := b.client.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filterArgs,
	})
	if err != nil {
		return fmt.Errorf("listing managed containers: %w", err)
	}

	if len(containers) == 0 {
		return nil
	}

	maxAge := b.cfg.Sessions.MaxDuration
	if maxAge == 0 {
		maxAge = 4 * time.Hour // Default max duration
	}

	now := time.Now()
	var cleaned int

	for _, c := range containers {
		createdAt := parseContainerCreatedAt(c.Labels, c.Created)
		if now.Sub(createdAt) <= maxAge {
			continue
		}

		// Container is expired, remove it.
		sessionID := c.Labels[LabelSessionID]
		ownerID := c.Labels[LabelOwnerID]

		b.log.WithFields(logrus.Fields{
			"container_id": c.ID[:12],
			"session_id":   sessionID,
			"owner_id":     ownerID,
		}).Info("Removing expired orphaned container")

		if err := b.forceRemoveContainer(ctx, c.ID); err != nil {
			b.log.WithFields(logrus.Fields{
				"container_id": c.ID[:12],
				"error":        err,
			}).Warn("Failed to remove expired container")

			continue
		}

		cleaned++
	}

	if cleaned > 0 {
		b.log.WithField("count", cleaned).Info("Cleaned up expired orphaned containers")
	}

	return nil
}

// ensureImage ensures the sandbox image is available locally.
func (b *DockerBackend) ensureImage(ctx context.Context) error {
	_, err := b.client.ImageInspect(ctx, b.cfg.Image)
	if err == nil {
		return nil
	}

	if !errdefs.IsNotFound(err) {
		return fmt.Errorf("inspecting image: %w", err)
	}

	// Image not found, try to pull it.
	b.log.WithField("image", b.cfg.Image).Info("Pulling sandbox image")

	reader, err := b.client.ImagePull(ctx, b.cfg.Image, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pulling image: %w", err)
	}
	defer func() { _ = reader.Close() }()

	// Consume the pull output.
	if _, err := io.Copy(io.Discard, reader); err != nil {
		return fmt.Errorf("reading pull output: %w", err)
	}

	return nil
}

// ensureNetwork ensures the configured Docker network exists.
// For user-defined networks, it checks if the network exists and creates it
// if missing. This enables running outside docker compose without requiring
// manual network creation. Built-in network modes (host, none, bridge,
// default) are skipped.
func (b *DockerBackend) ensureNetwork(ctx context.Context) error {
	networkMode := container.NetworkMode(b.cfg.Network)

	// Skip for empty or built-in network modes.
	if !networkMode.IsUserDefined() {
		return nil
	}

	networkName := b.cfg.Network
	log := b.log.WithField("network", networkName)

	// Check if the network already exists.
	_, err := b.client.NetworkInspect(ctx, networkName, network.InspectOptions{})
	if err == nil {
		log.Debug("Sandbox network exists")
		return nil
	}

	if !errdefs.IsNotFound(err) {
		return fmt.Errorf("inspecting network %q: %w", networkName, err)
	}

	// Network not found, create it.
	log.Info("Creating sandbox network")

	_, err = b.client.NetworkCreate(ctx, networkName, network.CreateOptions{
		Driver: "bridge",
		Labels: map[string]string{
			LabelManaged: "true",
		},
	})
	if err != nil {
		return fmt.Errorf("creating network %q: %w", networkName, err)
	}

	log.Info("Sandbox network created")

	return nil
}
