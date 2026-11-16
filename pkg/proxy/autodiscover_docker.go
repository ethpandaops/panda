package proxy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

const dockerResolveTimeout = 5 * time.Second

var errPublishedPortNotFound = errors.New("no running container publishes the port")

type publishedPortProbe func(ctx context.Context, host string, port int) error

type publishedPortResolver interface {
	Resolve(ctx context.Context, hostPort int, probe publishedPortProbe) (string, int, error)
}

type publishedPortCandidate struct {
	networkName string
	networkID   string
	host        string
	port        int
}

type dockerPublishedPortResolver struct {
	mu     sync.Mutex
	client *client.Client
}

func newDockerPublishedPortResolver() *dockerPublishedPortResolver {
	return &dockerPublishedPortResolver{}
}

func (r *dockerPublishedPortResolver) Resolve(ctx context.Context, hostPort int, probe publishedPortProbe) (string, int, error) {
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

	attached := make(map[string]struct{})
	if self.Container.NetworkSettings != nil {
		for name := range self.Container.NetworkSettings.Networks {
			attached[name] = struct{}{}
		}
	}

	candidates := publishedPortCandidates(list.Items, hostPort, self.Container.ID, attached)
	if len(candidates) == 0 {
		return "", 0, fmt.Errorf("%w: %d", errPublishedPortNotFound, hostPort)
	}

	errs := make([]error, 0, len(candidates))

	for _, candidate := range candidates {
		_, wasAttached := attached[candidate.networkName]
		if !wasAttached {
			if _, err := cli.NetworkConnect(ctx, candidate.networkID, client.NetworkConnectOptions{
				Container: self.Container.ID,
			}); err != nil {
				errs = append(errs, fmt.Errorf("connecting to network %q: %w", candidate.networkName, err))

				continue
			}
		}

		probeErr := probe(ctx, candidate.host, candidate.port)
		if probeErr == nil {
			return candidate.host, candidate.port, nil
		}

		errs = append(errs, fmt.Errorf("probing %s:%d on network %q: %w", candidate.host, candidate.port, candidate.networkName, probeErr))

		if !wasAttached {
			if _, err := cli.NetworkDisconnect(ctx, candidate.networkID, client.NetworkDisconnectOptions{
				Container: self.Container.ID,
			}); err != nil {
				errs = append(errs, fmt.Errorf("disconnecting from network %q: %w", candidate.networkName, err))
			}
		}
	}

	return "", 0, errors.Join(errs...)
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

func publishedPortCandidates(containers []container.Summary, hostPort int, selfID string, attached map[string]struct{}) []publishedPortCandidate {
	candidates := make([]publishedPortCandidate, 0, 1)

	for _, ctr := range containers {
		if ctr.ID == selfID || ctr.NetworkSettings == nil || !publishesTCPPort(ctr, hostPort) {
			continue
		}

		privatePort := publishedPrivatePort(ctr, hostPort)

		for name, endpoint := range ctr.NetworkSettings.Networks {
			if endpoint == nil || !endpoint.IPAddress.IsValid() {
				continue
			}

			candidates = append(candidates, publishedPortCandidate{
				networkName: name,
				networkID:   endpoint.NetworkID,
				host:        endpoint.IPAddress.String(),
				port:        privatePort,
			})
		}
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		_, iAttached := attached[candidates[i].networkName]
		_, jAttached := attached[candidates[j].networkName]

		if iAttached != jAttached {
			return iAttached
		}

		if candidates[i].networkName != candidates[j].networkName {
			return candidates[i].networkName < candidates[j].networkName
		}

		return candidates[i].host < candidates[j].host
	})

	return candidates
}

func publishesTCPPort(ctr container.Summary, hostPort int) bool {
	return publishedPrivatePort(ctr, hostPort) != 0
}

func publishedPrivatePort(ctr container.Summary, hostPort int) int {
	for _, port := range ctr.Ports {
		if int(port.PublicPort) == hostPort && port.Type == "tcp" {
			return int(port.PrivatePort)
		}
	}

	return 0
}
