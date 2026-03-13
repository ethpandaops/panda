package sandbox

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/sirupsen/logrus"
)

func TestCleanupExpiredContainersRemovesOnlyExpiredManagedContainers(t *testing.T) {
	t.Parallel()

	backend := &DockerBackend{
		cfg:              testSandboxConfig(),
		log:              logrus.New(),
		activeContainers: make(map[string]string),
	}
	backend.cfg.Sessions.MaxDuration = time.Hour

	var removed []string
	backend.containerListFunc = func(context.Context, container.ListOptions) ([]container.Summary, error) {
		now := time.Now()
		return []container.Summary{
			{ID: "expired-container", Labels: map[string]string{LabelCreatedAt: strconv.FormatInt(now.Add(-2*time.Hour).Unix(), 10)}},
			{ID: "fresh-container", Labels: map[string]string{LabelCreatedAt: strconv.FormatInt(now.Add(-30*time.Minute).Unix(), 10)}},
		}, nil
	}
	backend.containerRemoveAPIFunc = func(_ context.Context, containerID string, _ container.RemoveOptions) error {
		removed = append(removed, containerID)
		return nil
	}

	if err := backend.cleanupExpiredContainers(context.Background()); err != nil {
		t.Fatalf("cleanupExpiredContainers() error = %v", err)
	}
	if len(removed) != 1 || removed[0] != "expired-container" {
		t.Fatalf("removed = %v, want only expired-container", removed)
	}
}

func TestEnsureImageHandlesInspectPullAndReadErrors(t *testing.T) {
	t.Parallel()

	t.Run("inspect hit", func(t *testing.T) {
		t.Parallel()

		backend := &DockerBackend{cfg: testSandboxConfig(), log: logrus.New()}
		backend.imageInspectFunc = func(context.Context, string) error { return nil }

		if err := backend.ensureImage(context.Background()); err != nil {
			t.Fatalf("ensureImage() error = %v", err)
		}
	})

	t.Run("pull after not found", func(t *testing.T) {
		t.Parallel()

		backend := &DockerBackend{cfg: testSandboxConfig(), log: logrus.New()}
		backend.imageInspectFunc = func(context.Context, string) error { return errdefs.ErrNotFound }
		backend.imagePullFunc = func(context.Context, string) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("pulled")), nil
		}

		if err := backend.ensureImage(context.Background()); err != nil {
			t.Fatalf("ensureImage() error = %v", err)
		}
	})

	t.Run("inspect error", func(t *testing.T) {
		t.Parallel()

		backend := &DockerBackend{cfg: testSandboxConfig(), log: logrus.New()}
		backend.imageInspectFunc = func(context.Context, string) error { return errors.New("inspect failed") }

		if err := backend.ensureImage(context.Background()); err == nil || !strings.Contains(err.Error(), "inspecting image") {
			t.Fatalf("ensureImage() error = %v, want inspect failure", err)
		}
	})
}

func TestEnsureNetworkHandlesBuiltinAndUserDefinedModes(t *testing.T) {
	t.Parallel()

	t.Run("builtin skipped", func(t *testing.T) {
		t.Parallel()

		backend := &DockerBackend{cfg: testSandboxConfig(), log: logrus.New()}
		backend.cfg.Network = "bridge"
		called := false
		backend.networkInspectFunc = func(context.Context, string) error {
			called = true
			return nil
		}

		if err := backend.ensureNetwork(context.Background()); err != nil {
			t.Fatalf("ensureNetwork() error = %v", err)
		}
		if called {
			t.Fatal("ensureNetwork() inspected builtin bridge network")
		}
	})

	t.Run("user defined create", func(t *testing.T) {
		t.Parallel()

		backend := &DockerBackend{cfg: testSandboxConfig(), log: logrus.New()}
		backend.cfg.Network = "sandbox-net"
		backend.networkInspectFunc = func(context.Context, string) error { return errdefs.ErrNotFound }
		created := false
		backend.networkCreateFunc = func(_ context.Context, name string, opts network.CreateOptions) error {
			created = true
			if name != "sandbox-net" || opts.Driver != "bridge" {
				t.Fatalf("createNetwork() = (%q, %#v), want sandbox-net bridge", name, opts)
			}
			return nil
		}

		if err := backend.ensureNetwork(context.Background()); err != nil {
			t.Fatalf("ensureNetwork() error = %v", err)
		}
		if !created {
			t.Fatal("ensureNetwork() did not create missing user-defined network")
		}
	})
}
