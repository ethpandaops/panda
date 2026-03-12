package sandbox

import (
	"context"
	"errors"
	"testing"

	"github.com/docker/docker/api/types/system"
	"github.com/docker/docker/client"
	"github.com/sirupsen/logrus"
)

func TestNewGVisorBackendUsesGVisorSecurityConfig(t *testing.T) {
	t.Parallel()

	backend, err := NewGVisorBackend(testSandboxConfig(), logrus.New())
	if err != nil {
		t.Fatalf("NewGVisorBackend() error = %v", err)
	}

	securityCfg, err := backend.getSecurityConfig()
	if err != nil {
		t.Fatalf("getSecurityConfig() error = %v", err)
	}
	if securityCfg.Runtime != gVisorRuntimeName {
		t.Fatalf("Runtime = %q, want %q", securityCfg.Runtime, gVisorRuntimeName)
	}
}

func TestGVisorBackendVerifyRuntimeAndStart(t *testing.T) {
	t.Parallel()

	backend, err := NewGVisorBackend(testSandboxConfig(), logrus.New())
	if err != nil {
		t.Fatalf("NewGVisorBackend() error = %v", err)
	}

	backend.client = &client.Client{}
	backend.dockerInfoFunc = func(context.Context) (system.Info, error) {
		return system.Info{Runtimes: map[string]system.RuntimeWithStatus{gVisorRuntimeName: {}}}, nil
	}
	if err := backend.verifyGVisorRuntime(context.Background()); err != nil {
		t.Fatalf("verifyGVisorRuntime() error = %v", err)
	}

	backend = &GVisorBackend{DockerBackend: &DockerBackend{
		cfg:              testSandboxConfig(),
		log:              logrus.New(),
		activeContainers: make(map[string]string),
	}}
	backend.newClientFunc = func() (*client.Client, error) { return &client.Client{}, nil }
	backend.pingClientFunc = func(context.Context, *client.Client) error { return nil }
	backend.dockerInfoFunc = func(context.Context) (system.Info, error) {
		return system.Info{Runtimes: map[string]system.RuntimeWithStatus{gVisorRuntimeName: {}}}, nil
	}
	backend.ensureImageFunc = func(context.Context) error { return nil }
	backend.ensureNetworkFunc = func(context.Context) error { return nil }
	backend.startSessionManagerFunc = func(context.Context) error { return nil }

	if err := backend.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	backend.dockerInfoFunc = func(context.Context) (system.Info, error) {
		return system.Info{}, errors.New("info failed")
	}
	if err := backend.verifyGVisorRuntime(context.Background()); err == nil || err.Error() != "getting docker info: info failed" {
		t.Fatalf("verifyGVisorRuntime() error = %v, want wrapped info error", err)
	}
}
