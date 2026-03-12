package sandbox

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"time"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// executeWithNewSession creates a new session container and executes code in it.
func (b *DockerBackend) executeWithNewSession(ctx context.Context, req ExecuteRequest) (*ExecutionResult, error) {
	timeout := req.Timeout
	if timeout == 0 {
		timeout = time.Duration(b.cfg.Timeout) * time.Second
	}

	log := b.log.WithField("mode", "new-session")
	sessionID := b.sessionManager.GenerateSessionID()
	session, err := b.createManagedSession(ctx, sessionID, req.OwnerID, req.Env, log)
	if err != nil {
		return nil, fmt.Errorf("creating session container: %w", err)
	}

	result, err := b.execInContainer(ctx, session, req.Code, timeout, req.Env)
	if err != nil {
		return nil, fmt.Errorf("executing in session: %w", err)
	}

	return b.prepareSessionResult(ctx, result, session.ID, session.ContainerID), nil
}

// executeInSession executes code in an existing session container.
func (b *DockerBackend) executeInSession(ctx context.Context, req ExecuteRequest) (*ExecutionResult, error) {
	timeout := req.Timeout
	if timeout == 0 {
		timeout = time.Duration(b.cfg.Timeout) * time.Second
	}

	log := b.log.WithFields(logrus.Fields{
		"mode":       "existing-session",
		"session_id": req.SessionID,
	})

	session, err := b.sessionManager.Acquire(ctx, req.SessionID, req.OwnerID)
	if err != nil {
		return nil, fmt.Errorf("getting session: %w", err)
	}

	log.Debug("Executing in existing session")

	result, err := b.execInContainer(ctx, session, req.Code, timeout, req.Env)
	if err != nil {
		return nil, fmt.Errorf("executing in session: %w", err)
	}

	return b.prepareSessionResult(ctx, result, session.ID, session.ContainerID), nil
}

// execInContainer executes Python code inside a running session container.
func (b *DockerBackend) execInContainer(
	ctx context.Context,
	session *Session,
	code string,
	timeout time.Duration,
	env map[string]string,
) (*ExecutionResult, error) {
	executionID := uuid.New().String()
	log := b.log.WithFields(logrus.Fields{
		"execution_id": executionID,
		"session_id":   session.ID,
		"container_id": session.ContainerID,
	})

	execCtx, cancel := context.WithTimeout(ctx, timeout+5*time.Second)
	defer cancel()

	scriptPath := fmt.Sprintf("/tmp/script_%s.py", executionID)
	if err := b.writeSessionScript(execCtx, session.ContainerID, scriptPath, code); err != nil {
		return nil, err
	}

	startTime := time.Now()
	execResp, attachResp, err := b.startSessionExec(execCtx, session.ContainerID, scriptPath, executionID, env)
	if err != nil {
		return nil, err
	}
	defer attachResp.Close()

	stdout, stderr, err := b.readSessionExecOutput(execCtx, attachResp.Reader, session.ContainerID, scriptPath, timeout, log)
	if err != nil {
		return nil, err
	}

	inspectResp, err := b.client.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return nil, fmt.Errorf("inspecting exec: %w", err)
	}

	duration := time.Since(startTime).Seconds()
	b.cleanupSessionScript(ctx, session.ContainerID, scriptPath)

	log.WithFields(logrus.Fields{
		"exit_code": inspectResp.ExitCode,
		"duration":  duration,
	}).Debug("Session execution completed")

	return &ExecutionResult{
		Stdout:          stdout,
		Stderr:          stderr,
		ExitCode:        inspectResp.ExitCode,
		ExecutionID:     executionID,
		DurationSeconds: duration,
	}, nil
}

func (b *DockerBackend) writeSessionScript(
	ctx context.Context,
	containerID, scriptPath, code string,
) error {
	encoded := base64.StdEncoding.EncodeToString([]byte(code))
	writeCmd := []string{"sh", "-c", fmt.Sprintf("echo %s | base64 -d > %s", encoded, scriptPath)}

	writeResp, err := b.client.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          writeCmd,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return fmt.Errorf("creating write exec: %w", err)
	}

	if err := b.client.ContainerExecStart(ctx, writeResp.ID, container.ExecStartOptions{}); err != nil {
		return fmt.Errorf("starting write exec: %w", err)
	}

	return nil
}

func (b *DockerBackend) startSessionExec(
	ctx context.Context,
	containerID, scriptPath, executionID string,
	env map[string]string,
) (container.ExecCreateResponse, dockertypes.HijackedResponse, error) {
	execResp, err := b.client.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          []string{"python", scriptPath},
		AttachStdout: true,
		AttachStderr: true,
		Env:          buildSessionExecEnv(env, executionID),
	})
	if err != nil {
		return container.ExecCreateResponse{}, dockertypes.HijackedResponse{}, fmt.Errorf("creating exec: %w", err)
	}

	attachResp, err := b.client.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return container.ExecCreateResponse{}, dockertypes.HijackedResponse{}, fmt.Errorf("attaching to exec: %w", err)
	}

	return execResp, attachResp, nil
}

func buildSessionExecEnv(env map[string]string, executionID string) []string {
	execEnv := make([]string, 0, len(env)+1)
	for k, v := range env {
		if k == "ETHPANDAOPS_EXECUTION_ID" {
			continue
		}
		execEnv = append(execEnv, k+"="+v)
	}

	execEnv = append(execEnv, "ETHPANDAOPS_EXECUTION_ID="+executionID)

	return execEnv
}

func (b *DockerBackend) readSessionExecOutput(
	execCtx context.Context,
	reader io.Reader,
	containerID, scriptPath string,
	timeout time.Duration,
	log logrus.FieldLogger,
) (string, string, error) {
	var stdout, stderr bytes.Buffer
	done := make(chan error, 1)

	go func() {
		_, err := stdcopy.StdCopy(&stdout, &stderr, reader)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			log.WithError(err).Warn("Error reading exec output")
		}
	case <-execCtx.Done():
		log.Warn("Execution timed out, cleaning up script file")
		b.cleanupSessionScriptWithTimeout(containerID, scriptPath)
		return "", "", fmt.Errorf("execution timed out after %s", timeout)
	}

	return stdout.String(), stderr.String(), nil
}

func (b *DockerBackend) cleanupSessionScriptWithTimeout(containerID, scriptPath string) {
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cleanupCancel()
	b.cleanupSessionScript(cleanupCtx, containerID, scriptPath)
}

func (b *DockerBackend) cleanupSessionScript(ctx context.Context, containerID, scriptPath string) {
	cleanupResp, err := b.client.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd: []string{"rm", "-f", scriptPath},
	})
	if err != nil {
		return
	}

	_ = b.client.ContainerExecStart(ctx, cleanupResp.ID, container.ExecStartOptions{})
}

func (b *DockerBackend) prepareSessionResult(
	ctx context.Context,
	result *ExecutionResult,
	sessionID, containerID string,
) *ExecutionResult {
	result.SessionID = sessionID
	result.SessionTTLRemaining = b.sessionManager.TTLRemaining(sessionID)
	result.SessionFiles = b.collectSessionFiles(ctx, containerID)

	return result
}
