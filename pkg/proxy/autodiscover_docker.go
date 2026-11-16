package proxy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

const dockerResolveTimeout = 5 * time.Second

var errPublishedPortNotFound = errors.New("no running container publishes the port")

type publishedPortResolver interface {
	Resolve(ctx context.Context, hostPort int) (string, int, error)
}

type dockerPublishedPortResolver struct {
	mu     sync.Mutex
	client *client.Client
}

func newDockerPublishedPortResolver() *dockerPublishedPortResolver {
	return &dockerPublishedPortResolver{}
}

func (r *dockerPublishedPortResolver) Resolve(ctx context.Context, hostPort int) (string, int, error) {
	ctx, cancel := context.WithTimeout(ctx, dockerResolveTimeout)
	defer cancel()

	cli, err := r.dockerClient()
	if err != nil {
		return "", 0, err
	}

	hostname, err := os.Hostname()
	if err != nil {
		return "", 0, fmt.Errorf("getting hostname: %w", err)
	}

	self, err := cli.ContainerInspect(ctx, hostname, client.ContainerInspectOptions{})
	if err != nil {
		return "", 0, fmt.Errorf("inspecting own container: %w", err)
	}

	list, err := cli.ContainerList(ctx, client.ContainerListOptions{})
	if err != nil {
		return "", 0, fmt.Errorf("listing containers: %w", err)
	}

	networkName, networkID, host, privatePort, err := findPublishedPort(list.Items, hostPort)
	if err != nil {
		return "", 0, err
	}

	attached := false
	if self.Container.NetworkSettings != nil {
		_, attached = self.Container.NetworkSettings.Networks[networkName]
	}

	if !attached {
		if _, err := cli.NetworkConnect(ctx, networkID, client.NetworkConnectOptions{
			Container: self.Container.ID,
		}); err != nil {
			return "", 0, fmt.Errorf("connecting to network %q: %w", networkName, err)
		}
	}

	return host, privatePort, nil
}

func (r *dockerPublishedPortResolver) dockerClient() (*client.Client, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.client != nil {
		return r.client, nil
	}

	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("creating docker client: %w", err)
	}

	r.client = cli

	return cli, nil
}

func findPublishedPort(containers []container.Summary, hostPort int) (networkName, networkID, host string, privatePort int, err error) {
	for _, ctr := range containers {
		for _, port := range ctr.Ports {
			if int(port.PublicPort) != hostPort || port.Type != "tcp" {
				continue
			}

			if ctr.NetworkSettings == nil {
				continue
			}

			for name, endpoint := range ctr.NetworkSettings.Networks {
				if endpoint == nil || !endpoint.IPAddress.IsValid() {
					continue
				}

				return name, endpoint.NetworkID, endpoint.IPAddress.String(), int(port.PrivatePort), nil
			}
		}
	}

	return "", "", "", 0, fmt.Errorf("%w: %d", errPublishedPortNotFound, hostPort)
}
