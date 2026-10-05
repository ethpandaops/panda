package sandbox

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/moby/moby/client"
)

// A supervisor starts each script in its own process group. The lock makes
// cancellation atomic with launching the child, including a late Docker attach.
// Cancellation markers remain until the session container is removed so a
// delayed start cannot run after cleanup. They contain no user code or secrets.
const dockerSessionRunner = `
import fcntl, os, signal, subprocess, sys
script = sys.argv[1]
with open(script + ".lock", "w") as lock:
    fcntl.flock(lock, fcntl.LOCK_EX)
    if os.path.exists(script + ".cancel"):
        sys.exit(124)
    child = subprocess.Popen([sys.executable, script], start_new_session=True)
    with open(script + ".pid", "w") as pidfile:
        pidfile.write(str(child.pid))
    fcntl.flock(lock, fcntl.LOCK_UN)
    try:
        code = child.wait()
    finally:
        fcntl.flock(lock, fcntl.LOCK_EX)
        try:
            os.killpg(child.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        os.unlink(script + ".pid")
sys.exit(code if code >= 0 else 128 - code)
`

const dockerSessionCancel = `
import fcntl, os, signal, sys
script = sys.argv[1]
with open(script + ".lock", "w") as lock:
    fcntl.flock(lock, fcntl.LOCK_EX)
    open(script + ".cancel", "w").close()
    try:
        with open(script + ".pid") as pidfile:
            pid = int(pidfile.read())
        os.killpg(pid, signal.SIGKILL)
    except (FileNotFoundError, ProcessLookupError):
        pass
    try:
        os.unlink(script)
    except FileNotFoundError:
        pass
`

// stopSessionExecution explicitly terminates Python and its child processes;
// closing the Docker output attachment alone does not terminate an exec.
func (b *DockerBackend) stopSessionExecution(containerID, scriptPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	exec, err := b.client.ExecCreate(ctx, containerID, client.ExecCreateOptions{
		Cmd:          []string{"python", "-c", dockerSessionCancel, scriptPath},
		AttachStdout: true, AttachStderr: true,
	})
	if err != nil {
		return fmt.Errorf("creating cancellation exec: %w", err)
	}
	attached, err := b.client.ExecAttach(ctx, exec.ID, client.ExecAttachOptions{})
	if err != nil {
		return fmt.Errorf("attaching cancellation exec: %w", err)
	}
	defer attached.Close()
	// ExecAttach's context does not interrupt reads on the hijacked socket.
	done := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, attached.Reader); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("reading cancellation exec: %w", err)
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	inspected, err := b.client.ExecInspect(ctx, exec.ID, client.ExecInspectOptions{})
	if err != nil {
		return err
	}
	if inspected.ExitCode != 0 {
		return fmt.Errorf("cancellation exec exited %d", inspected.ExitCode)
	}
	return nil
}
