package sandbox

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/docker/docker/pkg/stdcopy"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFilterSessionEnv(t *testing.T) {
	t.Parallel()

	assert.Nil(t, filterSessionEnv(nil))

	filtered := filterSessionEnv(map[string]string{
		"KEEP":                  "value",
		"ETHPANDAOPS_API_TOKEN": "secret",
	})

	assert.Equal(t, map[string]string{"KEEP": "value"}, filtered)
}

func TestBuildSessionExecEnvReplacesExecutionID(t *testing.T) {
	t.Parallel()

	env := buildSessionExecEnv(map[string]string{
		"ALPHA":                    "beta",
		"ETHPANDAOPS_EXECUTION_ID": "stale",
	}, "fresh-id")

	assert.Contains(t, env, "ALPHA=beta")
	assert.Contains(t, env, "ETHPANDAOPS_EXECUTION_ID=fresh-id")

	for _, entry := range env {
		assert.NotEqual(t, "ETHPANDAOPS_EXECUTION_ID=stale", entry)
	}
}

func TestReadSessionExecOutputSplitsStdoutAndStderr(t *testing.T) {
	t.Parallel()

	var multiplexed bytes.Buffer

	stdoutWriter := stdcopy.NewStdWriter(&multiplexed, stdcopy.Stdout)
	stderrWriter := stdcopy.NewStdWriter(&multiplexed, stdcopy.Stderr)

	_, err := stdoutWriter.Write([]byte("hello stdout"))
	require.NoError(t, err)
	_, err = stderrWriter.Write([]byte("hello stderr"))
	require.NoError(t, err)

	backend := &DockerBackend{log: logrus.New()}

	stdout, stderr, err := backend.readSessionExecOutput(
		context.Background(),
		bytes.NewReader(multiplexed.Bytes()),
		"container-1",
		"/tmp/script.py",
		time.Second,
		logrus.New(),
	)
	require.NoError(t, err)
	assert.Equal(t, "hello stdout", stdout)
	assert.Equal(t, "hello stderr", stderr)
}
