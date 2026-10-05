package sandbox

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/config"
)

// Run explicitly with PANDA_TEST_DOCKER_IMAGE set to an installed sandbox image.
// Exercises real Docker exec cancellation, not a mocked attached wait.
func TestDockerSessionExecutionTerminates(t *testing.T) {
	image := os.Getenv("PANDA_TEST_DOCKER_IMAGE")
	if image == "" {
		t.Skip("set PANDA_TEST_DOCKER_IMAGE to run Docker integration test")
	}
	c, err := client.New(client.FromEnv)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, c.Close()) })
	ctx := context.Background()
	created, err := c.ContainerCreate(ctx, client.ContainerCreateOptions{Config: &container.Config{
		Image: image, Entrypoint: []string{"python"}, Cmd: []string{"-c", "import time; time.sleep(120)"},
	}})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := c.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true})
		require.NoError(t, err)
	})
	_, err = c.ContainerStart(ctx, created.ID, client.ContainerStartOptions{})
	require.NoError(t, err)
	b, err := NewDockerBackend(config.SandboxConfig{}, logrus.New())
	require.NoError(t, err)
	b.client = c
	session := &Session{ID: "test", Handle: created.ID}
	for _, mode := range []string{"timeout", "disconnect"} {
		t.Run(mode, func(t *testing.T) {
			execCtx := ctx
			if mode == "disconnect" {
				var cancel context.CancelFunc
				execCtx, cancel = context.WithTimeout(ctx, time.Second)
				defer cancel()
			}
			code := `import subprocess, sys, time
open("/tmp/started_` + mode + `", "w").close()
subprocess.Popen([sys.executable, "-c", 'import time; time.sleep(8); open("/tmp/child_` + mode + `", "w").close()'])
time.sleep(8)
open("/tmp/parent_` + mode + `", "w").close()
`
			_, err := b.execInContainer(execCtx, session, "cancel-"+mode, code, 100*time.Millisecond, nil)
			require.ErrorContains(t, err, "timed out")
		})
	}
	// Any surviving process would write its sentinel by now.
	time.Sleep(8 * time.Second)
	result, err := b.execInContainer(ctx, session, "check", `import os
for mode in ["timeout", "disconnect"]:
    assert os.path.exists("/tmp/started_" + mode), "script never started"
    assert not os.path.exists("/tmp/parent_" + mode), "parent survived cancellation"
    assert not os.path.exists("/tmp/child_" + mode), "child survived cancellation"
print("session still usable; canceled parents and children stopped")
`, 5*time.Second, nil)
	require.NoError(t, err)
	require.Zero(t, result.ExitCode, result.Stderr)
	require.Contains(t, result.Stdout, "session still usable")
	// Simulate failed post-success cleanup by making its marker unwritable as
	// a file. The completed execution must not kill the healthy session.
	result, err = b.execInContainer(ctx, session, "cleanup-failure", `import os
os.mkdir("/tmp/script_cleanup-failure.py.cancel")
print("finished successfully")
`, 5*time.Second, nil)
	require.NoError(t, err)
	require.Zero(t, result.ExitCode)
	result, err = b.execInContainer(ctx, session, "still-healthy", `print("healthy session preserved")`, 5*time.Second, nil)
	require.NoError(t, err)
	require.Zero(t, result.ExitCode)
	require.Contains(t, result.Stdout, "healthy session preserved")

}
