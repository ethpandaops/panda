//go:build linux

package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/config"
)

// End-to-end confinement checks; requireDirectExec skips them unless the full
// stack is present. Run the test binary in a privileged --init container.

func runConfined(t *testing.T, code string, env map[string]string) *ExecutionResult {
	t.Helper()
	requireDirectExec(t)

	b, err := NewDirectBackend(config.SandboxConfig{
		Timeout: 30,
		ExecUID: directExecTestUID,
		ExecGID: directExecTestUID,
	}, logrus.New())
	if err != nil {
		t.Fatalf("NewDirectBackend: %v", err)
	}
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = b.Stop(context.Background()) })

	// Timeout left zero so the backend uses cfg.Timeout (30s); a bare "30" here
	// would be 30 nanoseconds (time.Duration) and time out instantly.
	res, err := b.Execute(context.Background(), ExecuteRequest{Code: code, Env: env})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	return res
}

// The empty network namespace must have no route out: a raw TCP connect to a
// public address fails with ENETUNREACH. This is the anti-exfiltration boundary.
func TestConfinementBlocksNetworkEgress(t *testing.T) {
	res := runConfined(t, `
import socket
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
s.settimeout(3)
try:
    s.connect(("1.1.1.1", 443))
    print("REACHABLE")
except OSError as e:
    print("BLOCKED", e.errno)
`, nil)

	if !strings.Contains(res.Stdout, "BLOCKED") {
		t.Fatalf("expected TCP egress to be blocked, got stdout=%q stderr=%q", res.Stdout, res.Stderr)
	}
	// 101 == ENETUNREACH: the netns has no route, the defining symptom.
	if !strings.Contains(res.Stdout, "101") {
		t.Errorf("expected ENETUNREACH (101), got %q", res.Stdout)
	}
}

// The PID namespace must hide the server: the sandbox cannot see the server
// process in /proc, so /proc/<server-pid> and its environ are unreachable.
func TestConfinementHidesServerProcess(t *testing.T) {
	serverPID := strconv.Itoa(os.Getpid())

	res := runConfined(t, `
import os
sp = os.environ["SERVER_PID"]
pids = sorted(p for p in os.listdir("/proc") if p.isdigit())
print("SERVER_VISIBLE", os.path.exists("/proc/" + sp))
print("PIDCOUNT", len(pids))
`, map[string]string{"SERVER_PID": serverPID})

	if !strings.Contains(res.Stdout, "SERVER_VISIBLE False") {
		t.Errorf("server process leaked into the sandbox's /proc: %q", res.Stdout)
	}
}

// Landlock + the uid drop must keep server-owned secrets outside the workspace
// unreadable: a 0600 server file not on the allowlist is denied.
func TestConfinementCannotReadServerSecret(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "credentials")
	if err := os.WriteFile(secret, []byte("super-secret"), 0o600); err != nil {
		t.Fatalf("writing secret: %v", err)
	}

	res := runConfined(t, `
import os
p = os.environ["SECRET_PATH"]
try:
    with open(p) as f:
        print("READ", f.read())
except OSError as e:
    print("DENIED", e.errno)
`, map[string]string{"SECRET_PATH": secret})

	if strings.Contains(res.Stdout, "super-secret") {
		t.Fatalf("sandbox read a server secret it must not reach: %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "DENIED") {
		t.Errorf("expected the secret read to be denied, got %q", res.Stdout)
	}
}

// Positive control: the workspace itself must stay writable, or the confinement
// would be useless (nothing could run).
func TestConfinementWorkspaceWritable(t *testing.T) {
	res := runConfined(t, `
with open("proof.txt", "w") as f:
    f.write("ok")
with open("proof.txt") as f:
    print("WROTE", f.read())
`, nil)

	if !strings.Contains(res.Stdout, "WROTE ok") {
		t.Fatalf("workspace was not writable: stdout=%q stderr=%q", res.Stdout, res.Stderr)
	}
}
