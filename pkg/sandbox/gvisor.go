package sandbox

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types/system"
	"github.com/docker/docker/client"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/config"
)

// gVisorRuntimeName is the Docker runtime name for gVisor.
const gVisorRuntimeName = "runsc"

// GVisorBackend implements sandbox execution using Docker with gVisor runtime.
// gVisor provides user-space kernel isolation, making container escapes significantly
// harder compared to standard Docker. Only available on Linux.
type GVisorBackend struct {
	*DockerBackend
}

// NewGVisorBackend creates a new gVisor sandbox backend.
func NewGVisorBackend(cfg config.SandboxConfig, log logrus.FieldLogger) (*GVisorBackend, error) {
	dockerBackend, err := NewDockerBackend(cfg, log)
	if err != nil {
		return nil, err
	}

	// Override the component name in the logger.
	dockerBackend.log = log.WithField("component", "sandbox.gvisor")

	// Use gVisor security config which sets the runsc runtime.
	dockerBackend.securityConfigFunc = GVisorSecurityConfig

	return &GVisorBackend{
		DockerBackend: dockerBackend,
	}, nil
}

// Name returns the backend name.
func (b *GVisorBackend) Name() string {
	return "gvisor"
}

// Start initializes the Docker client and verifies gVisor runtime is available.
func (b *GVisorBackend) Start(ctx context.Context) error {
	b.log.Info("Starting gVisor sandbox backend")

	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("creating docker client: %w", err)
	}

	// Verify Docker is accessible.
	if _, err := dockerClient.Ping(ctx); err != nil {
		return fmt.Errorf("connecting to docker daemon: %w", err)
	}

	b.client = dockerClient

	// Verify gVisor runtime is available.
	if err := b.verifyGVisorRuntime(ctx); err != nil {
		return fmt.Errorf("verifying gvisor runtime: %w", err)
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

	b.log.WithField("image", b.cfg.Image).Info("gVisor sandbox backend started")

	return nil
}

// verifyGVisorRuntime checks that the gVisor (runsc) runtime is available.
func (b *GVisorBackend) verifyGVisorRuntime(ctx context.Context) error {
	info, err := b.client.Info(ctx)
	if err != nil {
		return fmt.Errorf("getting docker info: %w", err)
	}

	if !hasRuntime(info, gVisorRuntimeName) {
		return fmt.Errorf(
			"gVisor runtime '%s' not available; available runtimes: %v",
			gVisorRuntimeName,
			getRuntimeNames(info),
		)
	}

	b.log.Info("gVisor runtime verified")

	return nil
}

// hasRuntime checks if a specific runtime is available in Docker.
func hasRuntime(info system.Info, runtimeName string) bool {
	for name := range info.Runtimes {
		if name == runtimeName {
			return true
		}
	}

	return false
}

// getRuntimeNames returns a list of available runtime names for error messages.
func getRuntimeNames(info system.Info) []string {
	names := make([]string, 0, len(info.Runtimes))

	for name := range info.Runtimes {
		names = append(names, name)
	}

	return names
}
