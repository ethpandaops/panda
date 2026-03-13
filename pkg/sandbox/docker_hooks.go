package sandbox

import (
	"context"
	"io"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/system"
)

type dockerContainerCreateFunc func(context.Context, *container.Config, *container.HostConfig) (string, error)
type dockerContainerStartFunc func(context.Context, string) error
type dockerContainerWaitFunc func(context.Context, string, container.WaitCondition) (<-chan container.WaitResponse, <-chan error)
type dockerContainerLogsFunc func(context.Context, string, container.LogsOptions) (io.ReadCloser, error)
type dockerContainerKillFunc func(context.Context, string, string) error
type dockerContainerRemoveFunc func(context.Context, string, container.RemoveOptions) error
type dockerContainerListFunc func(context.Context, container.ListOptions) ([]container.Summary, error)
type dockerImageInspectFunc func(context.Context, string) error
type dockerImagePullFunc func(context.Context, string) (io.ReadCloser, error)
type dockerNetworkInspectFunc func(context.Context, string) error
type dockerNetworkCreateFunc func(context.Context, string, network.CreateOptions) error
type dockerInfoFunc func(context.Context) (system.Info, error)

func (b *DockerBackend) createContainer(
	ctx context.Context,
	containerConfig *container.Config,
	hostConfig *container.HostConfig,
) (string, error) {
	if b.containerCreateFunc != nil {
		return b.containerCreateFunc(ctx, containerConfig, hostConfig)
	}

	resp, err := b.client.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, "")
	if err != nil {
		return "", err
	}

	return resp.ID, nil
}

func (b *DockerBackend) startContainer(ctx context.Context, containerID string) error {
	if b.containerStartFunc != nil {
		return b.containerStartFunc(ctx, containerID)
	}

	return b.client.ContainerStart(ctx, containerID, container.StartOptions{})
}

func (b *DockerBackend) waitContainer(
	ctx context.Context,
	containerID string,
	condition container.WaitCondition,
) (<-chan container.WaitResponse, <-chan error) {
	if b.containerWaitFunc != nil {
		return b.containerWaitFunc(ctx, containerID, condition)
	}

	return b.client.ContainerWait(ctx, containerID, condition)
}

func (b *DockerBackend) readContainerLogs(
	ctx context.Context,
	containerID string,
	opts container.LogsOptions,
) (io.ReadCloser, error) {
	if b.containerLogsFunc != nil {
		return b.containerLogsFunc(ctx, containerID, opts)
	}

	return b.client.ContainerLogs(ctx, containerID, opts)
}

func (b *DockerBackend) killContainer(ctx context.Context, containerID, signal string) error {
	if b.containerKillFunc != nil {
		return b.containerKillFunc(ctx, containerID, signal)
	}

	return b.client.ContainerKill(ctx, containerID, signal)
}

func (b *DockerBackend) removeContainerAPI(ctx context.Context, containerID string, opts container.RemoveOptions) error {
	if b.containerRemoveAPIFunc != nil {
		return b.containerRemoveAPIFunc(ctx, containerID, opts)
	}

	return b.client.ContainerRemove(ctx, containerID, opts)
}

func (b *DockerBackend) listContainers(ctx context.Context, opts container.ListOptions) ([]container.Summary, error) {
	if b.containerListFunc != nil {
		return b.containerListFunc(ctx, opts)
	}

	return b.client.ContainerList(ctx, opts)
}

func (b *DockerBackend) inspectImage(ctx context.Context, imageName string) error {
	if b.imageInspectFunc != nil {
		return b.imageInspectFunc(ctx, imageName)
	}

	_, err := b.client.ImageInspect(ctx, imageName)

	return err
}

func (b *DockerBackend) pullImage(ctx context.Context, imageName string) (io.ReadCloser, error) {
	if b.imagePullFunc != nil {
		return b.imagePullFunc(ctx, imageName)
	}

	return b.client.ImagePull(ctx, imageName, image.PullOptions{})
}

func (b *DockerBackend) inspectNetwork(ctx context.Context, networkName string) error {
	if b.networkInspectFunc != nil {
		return b.networkInspectFunc(ctx, networkName)
	}

	_, err := b.client.NetworkInspect(ctx, networkName, network.InspectOptions{})

	return err
}

func (b *DockerBackend) createNetwork(ctx context.Context, networkName string, opts network.CreateOptions) error {
	if b.networkCreateFunc != nil {
		return b.networkCreateFunc(ctx, networkName, opts)
	}

	_, err := b.client.NetworkCreate(ctx, networkName, opts)

	return err
}

func (b *DockerBackend) dockerInfo(ctx context.Context) (system.Info, error) {
	if b.dockerInfoFunc != nil {
		return b.dockerInfoFunc(ctx)
	}

	return b.client.Info(ctx)
}
