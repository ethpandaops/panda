package sandbox

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/docker/docker/client"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/config"
)

func TestNewDockerBackendSessionCleanupUsesInjectedContainerRemoval(t *testing.T) {
	t.Parallel()

	backend, err := NewDockerBackend(testSandboxConfig(), logrus.New())
	if err != nil {
		t.Fatalf("NewDockerBackend() error = %v", err)
	}

	var removed []string
	backend.removeContainerFunc = func(_ context.Context, containerID string) error {
		removed = append(removed, containerID)
		return nil
	}

	if err := backend.sessionManager.cleanupCallback(context.Background(), "container-1"); err != nil {
		t.Fatalf("cleanupCallback(nil client) error = %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("removed containers = %v, want none with nil client", removed)
	}

	backend.client = &client.Client{}

	if err := backend.sessionManager.cleanupCallback(context.Background(), "container-2"); err != nil {
		t.Fatalf("cleanupCallback(client) error = %v", err)
	}
	if !slices.Equal(removed, []string{"container-2"}) {
		t.Fatalf("removed containers = %v, want [container-2]", removed)
	}
}

func TestDockerBackendStartUsesInjectedLifecycleHooks(t *testing.T) {
	t.Parallel()

	backend, err := NewDockerBackend(testSandboxConfig(), logrus.New())
	if err != nil {
		t.Fatalf("NewDockerBackend() error = %v", err)
	}

	fakeClient := &client.Client{}
	var calls []string

	backend.newClientFunc = func() (*client.Client, error) {
		calls = append(calls, "new-client")
		return fakeClient, nil
	}
	backend.pingClientFunc = func(_ context.Context, dockerClient *client.Client) error {
		calls = append(calls, "ping")
		if dockerClient != fakeClient {
			t.Fatalf("ping client = %p, want %p", dockerClient, fakeClient)
		}

		return nil
	}
	backend.cleanupExpiredContainersFunc = func(context.Context) error {
		calls = append(calls, "cleanup")
		return errors.New("stale containers")
	}
	backend.ensureImageFunc = func(context.Context) error {
		calls = append(calls, "image")
		return nil
	}
	backend.ensureNetworkFunc = func(context.Context) error {
		calls = append(calls, "network")
		return nil
	}
	backend.startSessionManagerFunc = func(context.Context) error {
		calls = append(calls, "sessions")
		return nil
	}

	if err := backend.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if backend.client != fakeClient {
		t.Fatalf("backend client = %p, want %p", backend.client, fakeClient)
	}

	want := []string{"new-client", "ping", "cleanup", "image", "network", "sessions"}
	if !slices.Equal(calls, want) {
		t.Fatalf("Start() calls = %v, want %v", calls, want)
	}
}

func TestDockerBackendStartWrapsLifecycleErrors(t *testing.T) {
	t.Parallel()

	t.Run("client creation", func(t *testing.T) {
		t.Parallel()

		backend, _ := NewDockerBackend(testSandboxConfig(), logrus.New())
		backend.newClientFunc = func() (*client.Client, error) {
			return nil, errors.New("boom")
		}

		if err := backend.Start(context.Background()); err == nil || err.Error() != "creating docker client: boom" {
			t.Fatalf("Start() error = %v, want wrapped client creation error", err)
		}
	})

	t.Run("docker ping", func(t *testing.T) {
		t.Parallel()

		backend, _ := NewDockerBackend(testSandboxConfig(), logrus.New())
		backend.newClientFunc = func() (*client.Client, error) {
			return &client.Client{}, nil
		}
		backend.pingClientFunc = func(context.Context, *client.Client) error {
			return errors.New("unreachable")
		}

		if err := backend.Start(context.Background()); err == nil || err.Error() != "connecting to docker daemon: unreachable" {
			t.Fatalf("Start() error = %v, want wrapped ping error", err)
		}
	})

	t.Run("image setup", func(t *testing.T) {
		t.Parallel()

		backend, _ := NewDockerBackend(testSandboxConfig(), logrus.New())
		backend.newClientFunc = func() (*client.Client, error) {
			return &client.Client{}, nil
		}
		backend.pingClientFunc = func(context.Context, *client.Client) error { return nil }
		backend.cleanupExpiredContainersFunc = func(context.Context) error { return nil }
		backend.ensureImageFunc = func(context.Context) error { return errors.New("missing image") }

		if err := backend.Start(context.Background()); err == nil || err.Error() != "ensuring sandbox image: missing image" {
			t.Fatalf("Start() error = %v, want wrapped image error", err)
		}
	})

	t.Run("network setup", func(t *testing.T) {
		t.Parallel()

		backend, _ := NewDockerBackend(testSandboxConfig(), logrus.New())
		backend.newClientFunc = func() (*client.Client, error) {
			return &client.Client{}, nil
		}
		backend.pingClientFunc = func(context.Context, *client.Client) error { return nil }
		backend.cleanupExpiredContainersFunc = func(context.Context) error { return nil }
		backend.ensureImageFunc = func(context.Context) error { return nil }
		backend.ensureNetworkFunc = func(context.Context) error { return errors.New("missing network") }

		if err := backend.Start(context.Background()); err == nil || err.Error() != "ensuring sandbox network: missing network" {
			t.Fatalf("Start() error = %v, want wrapped network error", err)
		}
	})

	t.Run("session manager", func(t *testing.T) {
		t.Parallel()

		backend, _ := NewDockerBackend(testSandboxConfig(), logrus.New())
		backend.newClientFunc = func() (*client.Client, error) {
			return &client.Client{}, nil
		}
		backend.pingClientFunc = func(context.Context, *client.Client) error { return nil }
		backend.cleanupExpiredContainersFunc = func(context.Context) error { return nil }
		backend.ensureImageFunc = func(context.Context) error { return nil }
		backend.ensureNetworkFunc = func(context.Context) error { return nil }
		backend.startSessionManagerFunc = func(context.Context) error { return errors.New("sessions unavailable") }

		if err := backend.Start(context.Background()); err == nil || err.Error() != "starting session manager: sessions unavailable" {
			t.Fatalf("Start() error = %v, want wrapped session manager error", err)
		}
	})
}

func TestDockerBackendStopCleansContainersAndClosesClient(t *testing.T) {
	t.Parallel()

	backend, err := NewDockerBackend(testSandboxConfig(), logrus.New())
	if err != nil {
		t.Fatalf("NewDockerBackend() error = %v", err)
	}

	backend.client = &client.Client{}
	backend.activeContainers = map[string]string{
		"exec-1": "container-1",
		"exec-2": "container-2",
	}

	var removed []string
	backend.stopSessionManagerFunc = func(context.Context) error {
		return errors.New("session cleanup failed")
	}
	backend.removeContainerFunc = func(_ context.Context, containerID string) error {
		removed = append(removed, containerID)
		if containerID == "container-2" {
			return errors.New("cannot remove container-2")
		}

		return nil
	}
	backend.closeClientFunc = func(dockerClient *client.Client) error {
		if dockerClient != backend.client {
			t.Fatalf("close client = %p, want %p", dockerClient, backend.client)
		}

		return nil
	}

	if err := backend.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	if len(backend.activeContainers) != 0 {
		t.Fatalf("activeContainers = %v, want empty", backend.activeContainers)
	}

	if !slices.Equal(removed, []string{"container-1", "container-2"}) && !slices.Equal(removed, []string{"container-2", "container-1"}) {
		t.Fatalf("removed containers = %v, want both tracked containers", removed)
	}
}

func TestDockerBackendStopReturnsCloseError(t *testing.T) {
	t.Parallel()

	backend, err := NewDockerBackend(testSandboxConfig(), logrus.New())
	if err != nil {
		t.Fatalf("NewDockerBackend() error = %v", err)
	}

	backend.client = &client.Client{}
	backend.stopSessionManagerFunc = func(context.Context) error { return nil }
	backend.closeClientFunc = func(*client.Client) error { return errors.New("close failed") }

	if err := backend.Stop(context.Background()); err == nil || err.Error() != "closing docker client: close failed" {
		t.Fatalf("Stop() error = %v, want wrapped close error", err)
	}
}

func TestDockerBackendExecuteRoutesByRequestMode(t *testing.T) {
	t.Parallel()

	okResult := &ExecutionResult{ExecutionID: "exec-1"}

	t.Run("existing session", func(t *testing.T) {
		t.Parallel()

		backend := &DockerBackend{
			client:         &client.Client{},
			sessionManager: &SessionManager{cfg: config.SessionConfig{}},
			executeInSessionFunc: func(_ context.Context, req ExecuteRequest) (*ExecutionResult, error) {
				if req.SessionID != "session-1" {
					t.Fatalf("SessionID = %q, want session-1", req.SessionID)
				}

				return okResult, nil
			},
		}

		result, err := backend.Execute(context.Background(), ExecuteRequest{SessionID: "session-1"})
		if err != nil {
			t.Fatalf("Execute(existing session) error = %v", err)
		}
		if result != okResult {
			t.Fatalf("Execute(existing session) result = %#v, want %#v", result, okResult)
		}
	})

	t.Run("new session", func(t *testing.T) {
		t.Parallel()

		enabled := true
		backend := &DockerBackend{
			client:         &client.Client{},
			sessionManager: &SessionManager{cfg: config.SessionConfig{Enabled: &enabled}},
			executeWithNewSessionFunc: func(context.Context, ExecuteRequest) (*ExecutionResult, error) {
				return okResult, nil
			},
		}

		result, err := backend.Execute(context.Background(), ExecuteRequest{})
		if err != nil {
			t.Fatalf("Execute(new session) error = %v", err)
		}
		if result != okResult {
			t.Fatalf("Execute(new session) result = %#v, want %#v", result, okResult)
		}
	})

	t.Run("ephemeral", func(t *testing.T) {
		t.Parallel()

		enabled := false
		backend := &DockerBackend{
			client:         &client.Client{},
			sessionManager: &SessionManager{cfg: config.SessionConfig{Enabled: &enabled}},
			executeEphemeralFunc: func(context.Context, ExecuteRequest) (*ExecutionResult, error) {
				return okResult, nil
			},
		}

		result, err := backend.Execute(context.Background(), ExecuteRequest{})
		if err != nil {
			t.Fatalf("Execute(ephemeral) error = %v", err)
		}
		if result != okResult {
			t.Fatalf("Execute(ephemeral) result = %#v, want %#v", result, okResult)
		}
	})
}
