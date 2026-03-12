package sandbox

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/sirupsen/logrus"
)

// parseContainerCreatedAt extracts the creation time from container labels.
// Falls back to Docker's created timestamp if the label is missing or invalid.
func parseContainerCreatedAt(labels map[string]string, dockerCreated int64) time.Time {
	if createdAtStr, ok := labels[LabelCreatedAt]; ok {
		if createdAtUnix, err := strconv.ParseInt(createdAtStr, 10, 64); err == nil {
			return time.Unix(createdAtUnix, 0)
		}
	}

	return time.Unix(dockerCreated, 0)
}

// getSecurityConfig returns the security configuration for this backend.
func (b *DockerBackend) getSecurityConfig() (*SecurityConfig, error) {
	return b.securityConfigFunc(b.cfg.MemoryLimit, b.cfg.CPULimit)
}

// cleanupExpiredContainers removes ethpandaops-panda containers that have exceeded max session duration.
// This handles orphaned containers from previous server instances that were killed abruptly.
func (b *DockerBackend) cleanupExpiredContainers(ctx context.Context) error {
	// Find all containers with our managed label.
	filterArgs := filters.NewArgs()
	filterArgs.Add("label", LabelManaged+"=true")

	containers, err := b.listContainers(ctx, container.ListOptions{
		All:     true,
		Filters: filterArgs,
	})
	if err != nil {
		return fmt.Errorf("listing managed containers: %w", err)
	}

	if len(containers) == 0 {
		return nil
	}

	maxAge := b.cfg.Sessions.MaxDuration
	if maxAge == 0 {
		maxAge = 4 * time.Hour // Default max duration
	}

	now := time.Now()
	var cleaned int

	for _, c := range containers {
		createdAt := parseContainerCreatedAt(c.Labels, c.Created)
		if now.Sub(createdAt) <= maxAge {
			continue
		}

		// Container is expired, remove it.
		sessionID := c.Labels[LabelSessionID]
		ownerID := c.Labels[LabelOwnerID]

		b.log.WithFields(logrus.Fields{
			"container_id": c.ID[:12],
			"session_id":   sessionID,
			"owner_id":     ownerID,
		}).Info("Removing expired orphaned container")

		if err := b.forceRemoveContainer(ctx, c.ID); err != nil {
			b.log.WithFields(logrus.Fields{
				"container_id": c.ID[:12],
				"error":        err,
			}).Warn("Failed to remove expired container")

			continue
		}

		cleaned++
	}

	if cleaned > 0 {
		b.log.WithField("count", cleaned).Info("Cleaned up expired orphaned containers")
	}

	return nil
}

// ensureImage ensures the sandbox image is available locally.
func (b *DockerBackend) ensureImage(ctx context.Context) error {
	err := b.inspectImage(ctx, b.cfg.Image)
	if err == nil {
		return nil
	}

	if !errdefs.IsNotFound(err) {
		return fmt.Errorf("inspecting image: %w", err)
	}

	// Image not found, try to pull it.
	b.log.WithField("image", b.cfg.Image).Info("Pulling sandbox image")

	reader, err := b.pullImage(ctx, b.cfg.Image)
	if err != nil {
		return fmt.Errorf("pulling image: %w", err)
	}
	defer func() { _ = reader.Close() }()

	// Consume the pull output.
	if _, err := io.Copy(io.Discard, reader); err != nil {
		return fmt.Errorf("reading pull output: %w", err)
	}

	return nil
}

// ensureNetwork ensures the configured Docker network exists.
// For user-defined networks, it checks if the network exists and creates it
// if missing. This enables stdio mode (outside docker compose) to work without
// requiring manual network creation. Built-in network modes (host, none,
// bridge, default) are skipped.
func (b *DockerBackend) ensureNetwork(ctx context.Context) error {
	networkMode := container.NetworkMode(b.cfg.Network)

	// Skip for empty or built-in network modes.
	if !networkMode.IsUserDefined() {
		return nil
	}

	networkName := b.cfg.Network
	log := b.log.WithField("network", networkName)

	// Check if the network already exists.
	err := b.inspectNetwork(ctx, networkName)
	if err == nil {
		log.Debug("Sandbox network exists")
		return nil
	}

	if !errdefs.IsNotFound(err) {
		return fmt.Errorf("inspecting network %q: %w", networkName, err)
	}

	// Network not found, create it.
	log.Info("Creating sandbox network")

	err = b.createNetwork(ctx, networkName, network.CreateOptions{
		Driver: "bridge",
		Labels: map[string]string{
			LabelManaged: "true",
		},
	})
	if err != nil {
		return fmt.Errorf("creating network %q: %w", networkName, err)
	}

	log.Info("Sandbox network created")

	return nil
}
