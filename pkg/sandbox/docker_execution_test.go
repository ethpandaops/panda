package sandbox

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/sirupsen/logrus"
)

func TestWaitForContainerAndLogs(t *testing.T) {
	t.Parallel()

	var multiplexed bytes.Buffer
	stdoutWriter := stdcopy.NewStdWriter(&multiplexed, stdcopy.Stdout)
	stderrWriter := stdcopy.NewStdWriter(&multiplexed, stdcopy.Stderr)
	_, _ = stdoutWriter.Write([]byte("stdout"))
	_, _ = stderrWriter.Write([]byte("stderr"))

	backend := &DockerBackend{log: logrus.New()}
	backend.containerWaitFunc = func(context.Context, string, container.WaitCondition) (<-chan container.WaitResponse, <-chan error) {
		statusCh := make(chan container.WaitResponse, 1)
		errCh := make(chan error, 1)
		statusCh <- container.WaitResponse{StatusCode: 7}
		return statusCh, errCh
	}
	backend.containerLogsFunc = func(context.Context, string, container.LogsOptions) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(multiplexed.Bytes())), nil
	}

	result, err := backend.waitForContainer(context.Background(), "container-1", time.Second)
	if err != nil {
		t.Fatalf("waitForContainer() error = %v", err)
	}
	if result.exitCode != 7 || result.stdout != "stdout" || result.stderr != "stderr" {
		t.Fatalf("waitForContainer() = %#v, want exit code and split logs", result)
	}
}

func TestExecuteEphemeralSuccessAndTimeout(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		backend := &DockerBackend{
			cfg:                testSandboxConfig(),
			log:                logrus.New(),
			activeContainers:   make(map[string]string),
			securityConfigFunc: DefaultSecurityConfig,
		}
		backend.containerCreateFunc = func(context.Context, *container.Config, *container.HostConfig) (string, error) {
			return "container-1", nil
		}
		backend.containerStartFunc = func(context.Context, string) error { return nil }
		backend.containerWaitFunc = func(context.Context, string, container.WaitCondition) (<-chan container.WaitResponse, <-chan error) {
			statusCh := make(chan container.WaitResponse, 1)
			errCh := make(chan error, 1)
			statusCh <- container.WaitResponse{StatusCode: 0}
			return statusCh, errCh
		}
		backend.containerLogsFunc = func(context.Context, string, container.LogsOptions) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(nil)), nil
		}
		removed := 0
		backend.containerRemoveAPIFunc = func(context.Context, string, container.RemoveOptions) error {
			removed++
			return nil
		}

		result, err := backend.executeEphemeral(context.Background(), ExecuteRequest{Code: "print('ok')", Timeout: 10 * time.Millisecond})
		if err != nil {
			t.Fatalf("executeEphemeral() error = %v", err)
		}
		if result.ExitCode != 0 || result.ExecutionID == "" {
			t.Fatalf("executeEphemeral() = %#v, want exit code and execution id", result)
		}
		if removed == 0 {
			t.Fatal("executeEphemeral() did not remove container")
		}
	})

	t.Run("timeout kills container", func(t *testing.T) {
		t.Parallel()

		backend := &DockerBackend{
			cfg:                testSandboxConfig(),
			log:                logrus.New(),
			activeContainers:   make(map[string]string),
			securityConfigFunc: DefaultSecurityConfig,
		}
		backend.containerCreateFunc = func(context.Context, *container.Config, *container.HostConfig) (string, error) {
			return "container-2", nil
		}
		backend.containerStartFunc = func(context.Context, string) error { return nil }
		backend.containerWaitFunc = func(ctx context.Context, _ string, _ container.WaitCondition) (<-chan container.WaitResponse, <-chan error) {
			statusCh := make(chan container.WaitResponse)
			errCh := make(chan error)
			go func() {
				<-ctx.Done()
			}()
			return statusCh, errCh
		}
		killed := false
		backend.containerKillFunc = func(context.Context, string, string) error {
			killed = true
			return nil
		}
		backend.containerRemoveAPIFunc = func(context.Context, string, container.RemoveOptions) error { return nil }

		_, err := backend.executeEphemeral(context.Background(), ExecuteRequest{Code: "print('slow')", Timeout: time.Millisecond})
		if err == nil || !strings.Contains(err.Error(), "execution timed out") {
			t.Fatalf("executeEphemeral() error = %v, want timeout", err)
		}
		if !killed {
			t.Fatal("executeEphemeral() did not kill timed out container")
		}
	})
}

func TestForceKillAndRemoveIgnoreNotFound(t *testing.T) {
	t.Parallel()

	backend := &DockerBackend{}
	backend.containerKillFunc = func(context.Context, string, string) error { return errdefs.ErrNotFound }
	backend.containerRemoveAPIFunc = func(context.Context, string, container.RemoveOptions) error { return errdefs.ErrNotFound }

	if err := backend.forceKillContainer(context.Background(), "container-1"); err != nil {
		t.Fatalf("forceKillContainer() error = %v", err)
	}
	if err := backend.forceRemoveContainer(context.Background(), "container-1"); err != nil {
		t.Fatalf("forceRemoveContainer() error = %v", err)
	}

	backend.containerKillFunc = func(context.Context, string, string) error { return errors.New("boom") }
	if err := backend.forceKillContainer(context.Background(), "container-2"); err == nil || !strings.Contains(err.Error(), "killing container") {
		t.Fatalf("forceKillContainer() error = %v, want wrapped error", err)
	}
}
