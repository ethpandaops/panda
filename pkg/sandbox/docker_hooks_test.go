package sandbox

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/system"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDockerHookOverrides(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	backend := &DockerBackend{}

	backend.containerCreateFunc = func(context.Context, *container.Config, *container.HostConfig) (string, error) {
		return "container-1", nil
	}
	containerID, err := backend.createContainer(ctx, &container.Config{}, &container.HostConfig{})
	require.NoError(t, err)
	assert.Equal(t, "container-1", containerID)

	backend.containerStartFunc = func(context.Context, string) error { return nil }
	require.NoError(t, backend.startContainer(ctx, "container-1"))

	waitResponseCh := make(chan container.WaitResponse, 1)
	waitErrorCh := make(chan error, 1)
	waitResponseCh <- container.WaitResponse{StatusCode: 0}
	waitErr := errors.New("wait error")
	waitErrorCh <- waitErr
	backend.containerWaitFunc = func(context.Context, string, container.WaitCondition) (<-chan container.WaitResponse, <-chan error) {
		return waitResponseCh, waitErrorCh
	}
	gotWaitResponseCh, gotWaitErrorCh := backend.waitContainer(ctx, "container-1", container.WaitConditionNotRunning)
	assert.Equal(t, container.WaitResponse{StatusCode: 0}, <-gotWaitResponseCh)
	assert.ErrorIs(t, <-gotWaitErrorCh, waitErr)

	backend.containerLogsFunc = func(context.Context, string, container.LogsOptions) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("logs")), nil
	}
	logs, err := backend.readContainerLogs(ctx, "container-1", container.LogsOptions{})
	require.NoError(t, err)
	logData, err := io.ReadAll(logs)
	require.NoError(t, err)
	require.NoError(t, logs.Close())
	assert.Equal(t, "logs", string(logData))

	backend.containerKillFunc = func(context.Context, string, string) error { return nil }
	require.NoError(t, backend.killContainer(ctx, "container-1", "SIGKILL"))

	backend.containerRemoveAPIFunc = func(context.Context, string, container.RemoveOptions) error { return nil }
	require.NoError(t, backend.removeContainerAPI(ctx, "container-1", container.RemoveOptions{Force: true}))

	backend.containerListFunc = func(context.Context, container.ListOptions) ([]container.Summary, error) {
		return []container.Summary{{ID: "container-1"}}, nil
	}
	containers, err := backend.listContainers(ctx, container.ListOptions{All: true})
	require.NoError(t, err)
	require.Len(t, containers, 1)
	assert.Equal(t, "container-1", containers[0].ID)

	backend.imageInspectFunc = func(context.Context, string) error { return nil }
	require.NoError(t, backend.inspectImage(ctx, "python:3.11"))

	backend.imagePullFunc = func(context.Context, string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("pull")), nil
	}
	pullReader, err := backend.pullImage(ctx, "python:3.11")
	require.NoError(t, err)
	pullData, err := io.ReadAll(pullReader)
	require.NoError(t, err)
	require.NoError(t, pullReader.Close())
	assert.Equal(t, "pull", string(pullData))

	backend.networkInspectFunc = func(context.Context, string) error { return nil }
	require.NoError(t, backend.inspectNetwork(ctx, "panda-net"))

	backend.networkCreateFunc = func(context.Context, string, network.CreateOptions) error { return nil }
	require.NoError(t, backend.createNetwork(ctx, "panda-net", network.CreateOptions{}))

	backend.dockerInfoFunc = func(context.Context) (system.Info, error) {
		return system.Info{Containers: 1}, nil
	}
	info, err := backend.dockerInfo(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, info.Containers)
}
