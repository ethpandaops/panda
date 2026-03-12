package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/sirupsen/logrus"
)

func (b *DockerBackend) createManagedSession(
	ctx context.Context,
	sessionID, ownerID string,
	env map[string]string,
	log logrus.FieldLogger,
) (*Session, error) {
	log = log.WithField("session_id", sessionID)
	log.Debug("Creating new session")

	containerID, err := b.createSessionContainer(ctx, sessionID, env, ownerID)
	if err != nil {
		return nil, err
	}

	b.sessionManager.RecordAccess(sessionID)
	log.Info("Created new session")

	now := time.Now()

	return &Session{
		ID:          sessionID,
		OwnerID:     ownerID,
		ContainerID: containerID,
		CreatedAt:   now,
		LastUsed:    now,
	}, nil
}

// createSessionContainer creates a long-running container for session use.
// sessionID is stored in container labels for stateless session recovery.
func (b *DockerBackend) createSessionContainer(ctx context.Context, sessionID string, env map[string]string, ownerID string) (string, error) {
	containerEnv := SandboxEnvDefaults()
	for k, v := range filterSessionEnv(env) {
		containerEnv[k] = v
	}

	envSlice := make([]string, 0, len(containerEnv))
	for k, v := range containerEnv {
		envSlice = append(envSlice, k+"="+v)
	}

	labels := map[string]string{
		LabelManaged:   "true",
		LabelSessionID: sessionID,
		LabelCreatedAt: strconv.FormatInt(time.Now().Unix(), 10),
	}
	if ownerID != "" {
		labels[LabelOwnerID] = ownerID
	}

	containerConfig := &container.Config{
		Image:      b.cfg.Image,
		Cmd:        []string{"sleep", "infinity"},
		Env:        envSlice,
		User:       "nobody",
		WorkingDir: "/workspace",
		Labels:     labels,
	}

	hostConfig := &container.HostConfig{
		NetworkMode: container.NetworkMode(b.cfg.Network),
		ExtraHosts:  []string{"host.docker.internal:host-gateway"},
	}

	securityCfg, err := b.getSecurityConfig()
	if err != nil {
		return "", fmt.Errorf("getting security config: %w", err)
	}
	securityCfg.ReadonlyRootfs = false
	securityCfg.ApplyToHostConfig(hostConfig)

	resp, err := b.client.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, "")
	if err != nil {
		return "", fmt.Errorf("creating container: %w", err)
	}

	if err := b.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		_ = b.forceRemoveContainer(ctx, resp.ID)
		return "", fmt.Errorf("starting container: %w", err)
	}

	if err := b.createSessionDirs(ctx, resp.ID); err != nil {
		_ = b.forceRemoveContainer(ctx, resp.ID)
		return "", fmt.Errorf("creating session directories: %w", err)
	}

	return resp.ID, nil
}

func filterSessionEnv(env map[string]string) map[string]string {
	if env == nil {
		return nil
	}

	filtered := make(map[string]string, len(env))
	for k, v := range env {
		if k == "ETHPANDAOPS_API_TOKEN" {
			continue
		}
		filtered[k] = v
	}

	return filtered
}

// createSessionDirs creates the workspace and output directories inside a session container.
func (b *DockerBackend) createSessionDirs(ctx context.Context, containerID string) error {
	execConfig := container.ExecOptions{
		Cmd:          []string{"sh", "-c", "mkdir -p /workspace /output && chmod 777 /workspace /output"},
		AttachStdout: true,
		AttachStderr: true,
		User:         "root",
	}

	execResp, err := b.client.ContainerExecCreate(ctx, containerID, execConfig)
	if err != nil {
		return fmt.Errorf("creating exec: %w", err)
	}

	if err := b.client.ContainerExecStart(ctx, execResp.ID, container.ExecStartOptions{}); err != nil {
		return fmt.Errorf("starting exec: %w", err)
	}

	return nil
}

// collectSessionFiles lists files in the session's /workspace directory.
func (b *DockerBackend) collectSessionFiles(ctx context.Context, containerID string) []SessionFile {
	execConfig := container.ExecOptions{
		Cmd:          []string{"find", "/workspace", "-maxdepth", "1", "-type", "f", "-printf", "%f\\t%s\\t%T@\\n"},
		AttachStdout: true,
		AttachStderr: true,
	}

	execResp, err := b.client.ContainerExecCreate(ctx, containerID, execConfig)
	if err != nil {
		b.log.WithError(err).Debug("Failed to create exec for listing session files")
		return nil
	}

	attachResp, err := b.client.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		b.log.WithError(err).Debug("Failed to attach to exec for listing session files")
		return nil
	}
	defer attachResp.Close()

	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, attachResp.Reader); err != nil {
		b.log.WithError(err).Debug("Failed to read session files list")
		return nil
	}

	files := make([]SessionFile, 0)
	lines := bytes.Split(bytes.TrimSpace(stdout.Bytes()), []byte("\n"))
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}

		parts := bytes.Split(line, []byte("\t"))
		if len(parts) != 3 {
			continue
		}

		var size int64
		var modTime float64

		if _, err := fmt.Sscanf(string(parts[1]), "%d", &size); err != nil {
			continue
		}
		if _, err := fmt.Sscanf(string(parts[2]), "%f", &modTime); err != nil {
			continue
		}

		files = append(files, SessionFile{
			Name:     string(parts[0]),
			Size:     size,
			Modified: time.Unix(int64(modTime), 0),
		})
	}

	return files
}

// getSessionContainer queries Docker for a session container by session ID.
// Returns nil if not found.
func (b *DockerBackend) getSessionContainer(ctx context.Context, sessionID string) (*SessionContainer, error) {
	if b.client == nil {
		return nil, fmt.Errorf("docker client not initialized")
	}

	filterArgs := filters.NewArgs()
	filterArgs.Add("label", LabelManaged+"=true")
	filterArgs.Add("label", LabelSessionID+"="+sessionID)

	containers, err := b.client.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filterArgs,
	})
	if err != nil {
		return nil, fmt.Errorf("listing containers: %w", err)
	}

	if len(containers) == 0 {
		return nil, nil
	}

	c := containers[0]
	return &SessionContainer{
		ContainerID: c.ID,
		SessionID:   sessionID,
		OwnerID:     c.Labels[LabelOwnerID],
		CreatedAt:   parseContainerCreatedAt(c.Labels, c.Created),
	}, nil
}

// listAllSessionContainers queries Docker for all session containers.
func (b *DockerBackend) listAllSessionContainers(ctx context.Context) ([]*SessionContainer, error) {
	if b.client == nil {
		return nil, fmt.Errorf("docker client not initialized")
	}

	filterArgs := filters.NewArgs()
	filterArgs.Add("label", LabelManaged+"=true")
	filterArgs.Add("label", LabelSessionID)

	containers, err := b.client.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filterArgs,
	})
	if err != nil {
		return nil, fmt.Errorf("listing containers: %w", err)
	}

	result := make([]*SessionContainer, 0, len(containers))
	for _, c := range containers {
		sessionID := c.Labels[LabelSessionID]
		if sessionID == "" {
			continue
		}

		result = append(result, &SessionContainer{
			ContainerID: c.ID,
			SessionID:   sessionID,
			OwnerID:     c.Labels[LabelOwnerID],
			CreatedAt:   parseContainerCreatedAt(c.Labels, c.Created),
		})
	}

	return result, nil
}
