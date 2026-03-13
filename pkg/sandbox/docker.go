package sandbox

import (
	"context"
	"fmt"
	"sync"

	"github.com/docker/docker/client"
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

// SecurityConfigFunc is a function that returns security configuration.
type SecurityConfigFunc func(memoryLimit string, cpuLimit float64) (*SecurityConfig, error)

type dockerClientFactory func() (*client.Client, error)
type dockerClientPinger func(context.Context, *client.Client) error
type dockerLifecycleFunc func(context.Context) error
type dockerContainerCleanupFunc func(context.Context, string) error
type dockerClientCloser func(*client.Client) error
type dockerExecutionFunc func(context.Context, ExecuteRequest) (*ExecutionResult, error)

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

	newClientFunc                dockerClientFactory
	pingClientFunc               dockerClientPinger
	cleanupExpiredContainersFunc dockerLifecycleFunc
	ensureImageFunc              dockerLifecycleFunc
	ensureNetworkFunc            dockerLifecycleFunc
	startSessionManagerFunc      dockerLifecycleFunc
	stopSessionManagerFunc       dockerLifecycleFunc
	removeContainerFunc          dockerContainerCleanupFunc
	closeClientFunc              dockerClientCloser
	executeInSessionFunc         dockerExecutionFunc
	executeWithNewSessionFunc    dockerExecutionFunc
	executeEphemeralFunc         dockerExecutionFunc
	containerCreateFunc          dockerContainerCreateFunc
	containerStartFunc           dockerContainerStartFunc
	containerWaitFunc            dockerContainerWaitFunc
	containerLogsFunc            dockerContainerLogsFunc
	containerKillFunc            dockerContainerKillFunc
	containerRemoveAPIFunc       dockerContainerRemoveFunc
	containerListFunc            dockerContainerListFunc
	imageInspectFunc             dockerImageInspectFunc
	imagePullFunc                dockerImagePullFunc
	networkInspectFunc           dockerNetworkInspectFunc
	networkCreateFunc            dockerNetworkCreateFunc
	dockerInfoFunc               dockerInfoFunc
}

// NewDockerBackend creates a new Docker sandbox backend.
func NewDockerBackend(cfg config.SandboxConfig, log logrus.FieldLogger) (*DockerBackend, error) {
	backend := &DockerBackend{
		cfg:                cfg,
		log:                log.WithField("component", "sandbox.docker"),
		activeContainers:   make(map[string]string, 16),
		securityConfigFunc: DefaultSecurityConfig,
	}
	backend.newClientFunc = func() (*client.Client, error) {
		return client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	}
	backend.pingClientFunc = func(ctx context.Context, dockerClient *client.Client) error {
		_, err := dockerClient.Ping(ctx)
		return err
	}
	backend.cleanupExpiredContainersFunc = backend.cleanupExpiredContainers
	backend.ensureImageFunc = backend.ensureImage
	backend.ensureNetworkFunc = backend.ensureNetwork
	backend.removeContainerFunc = backend.forceRemoveContainer
	backend.closeClientFunc = func(dockerClient *client.Client) error {
		if dockerClient == nil {
			return nil
		}

		return dockerClient.Close()
	}
	backend.executeInSessionFunc = backend.executeInSession
	backend.executeWithNewSessionFunc = backend.executeWithNewSession
	backend.executeEphemeralFunc = backend.executeEphemeral

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

			return backend.removeContainer(ctx, containerID)
		},
	)
	backend.startSessionManagerFunc = backend.sessionManager.Start
	backend.stopSessionManagerFunc = backend.sessionManager.Stop

	return backend, nil
}

// Name returns the backend name.
func (b *DockerBackend) Name() string {
	return "docker"
}

// Start initializes the Docker client and verifies connectivity.
func (b *DockerBackend) Start(ctx context.Context) error {
	b.log.Info("Starting Docker sandbox backend")

	dockerClient, err := b.newDockerClient()
	if err != nil {
		return fmt.Errorf("creating docker client: %w", err)
	}

	// Verify Docker is accessible.
	if err := b.pingDockerClient(ctx, dockerClient); err != nil {
		return fmt.Errorf("connecting to docker daemon: %w", err)
	}

	b.client = dockerClient

	// Clean up expired orphaned containers from previous runs.
	// Only removes containers older than max session duration to avoid
	// disrupting active sessions from other server instances.
	if err := b.cleanupExpired(ctx); err != nil {
		b.log.WithError(err).Warn("Failed to cleanup expired containers")
	}

	// Ensure the sandbox image is available.
	if err := b.ensureSandboxImage(ctx); err != nil {
		return fmt.Errorf("ensuring sandbox image: %w", err)
	}

	// Ensure the configured network exists (auto-creates for stdio mode).
	if err := b.ensureSandboxNetwork(ctx); err != nil {
		return fmt.Errorf("ensuring sandbox network: %w", err)
	}

	// Start session manager if enabled.
	if err := b.startSessionManager(ctx); err != nil {
		return fmt.Errorf("starting session manager: %w", err)
	}

	b.log.WithField("image", b.cfg.Image).Info("Docker sandbox backend started")

	return nil
}

// Stop cleans up any active containers and closes the Docker client.
func (b *DockerBackend) Stop(ctx context.Context) error {
	b.log.Info("Stopping Docker sandbox backend")

	// Stop session manager first (this will cleanup session containers).
	if err := b.stopSessionManager(ctx); err != nil {
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
		if err := b.removeContainer(ctx, containerID); err != nil {
			b.log.WithFields(logrus.Fields{
				"execution_id": execID,
				"container_id": containerID,
				"error":        err,
			}).Warn("Failed to remove container during shutdown")
		}
	}

	if b.client != nil {
		if err := b.closeClient(b.client); err != nil {
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
		return b.executeInSessionRequest(ctx, req)
	}

	// If sessions are enabled, create a new session.
	if b.sessionManager.Enabled() {
		return b.executeWithNewSessionRequest(ctx, req)
	}

	// Ephemeral execution (original behavior).
	return b.executeEphemeralRequest(ctx, req)
}

func (b *DockerBackend) newDockerClient() (*client.Client, error) {
	if b.newClientFunc != nil {
		return b.newClientFunc()
	}

	return client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
}

func (b *DockerBackend) pingDockerClient(ctx context.Context, dockerClient *client.Client) error {
	if b.pingClientFunc != nil {
		return b.pingClientFunc(ctx, dockerClient)
	}

	if dockerClient == nil {
		return fmt.Errorf("docker client is nil")
	}

	_, err := dockerClient.Ping(ctx)
	return err
}

func (b *DockerBackend) cleanupExpired(ctx context.Context) error {
	if b.cleanupExpiredContainersFunc != nil {
		return b.cleanupExpiredContainersFunc(ctx)
	}

	return b.cleanupExpiredContainers(ctx)
}

func (b *DockerBackend) ensureSandboxImage(ctx context.Context) error {
	if b.ensureImageFunc != nil {
		return b.ensureImageFunc(ctx)
	}

	return b.ensureImage(ctx)
}

func (b *DockerBackend) ensureSandboxNetwork(ctx context.Context) error {
	if b.ensureNetworkFunc != nil {
		return b.ensureNetworkFunc(ctx)
	}

	return b.ensureNetwork(ctx)
}

func (b *DockerBackend) startSessionManager(ctx context.Context) error {
	if b.startSessionManagerFunc != nil {
		return b.startSessionManagerFunc(ctx)
	}

	return b.sessionManager.Start(ctx)
}

func (b *DockerBackend) stopSessionManager(ctx context.Context) error {
	if b.stopSessionManagerFunc != nil {
		return b.stopSessionManagerFunc(ctx)
	}

	return b.sessionManager.Stop(ctx)
}

func (b *DockerBackend) removeContainer(ctx context.Context, containerID string) error {
	if b.removeContainerFunc != nil {
		return b.removeContainerFunc(ctx, containerID)
	}

	return b.forceRemoveContainer(ctx, containerID)
}

func (b *DockerBackend) closeClient(dockerClient *client.Client) error {
	if b.closeClientFunc != nil {
		return b.closeClientFunc(dockerClient)
	}

	if dockerClient == nil {
		return nil
	}

	return dockerClient.Close()
}

func (b *DockerBackend) executeInSessionRequest(ctx context.Context, req ExecuteRequest) (*ExecutionResult, error) {
	if b.executeInSessionFunc != nil {
		return b.executeInSessionFunc(ctx, req)
	}

	return b.executeInSession(ctx, req)
}

func (b *DockerBackend) executeWithNewSessionRequest(ctx context.Context, req ExecuteRequest) (*ExecutionResult, error) {
	if b.executeWithNewSessionFunc != nil {
		return b.executeWithNewSessionFunc(ctx, req)
	}

	return b.executeWithNewSession(ctx, req)
}

func (b *DockerBackend) executeEphemeralRequest(ctx context.Context, req ExecuteRequest) (*ExecutionResult, error) {
	if b.executeEphemeralFunc != nil {
		return b.executeEphemeralFunc(ctx, req)
	}

	return b.executeEphemeral(ctx, req)
}
