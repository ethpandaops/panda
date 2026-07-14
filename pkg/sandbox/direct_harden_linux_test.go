//go:build linux

package sandbox

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/ethpandaops/panda/pkg/config"
)

// TestPreflightDirectHardeningFailsClosed covers the config-level checks that do
// not depend on the host's capabilities, so they are deterministic everywhere.
func TestPreflightDirectHardeningFailsClosed(t *testing.T) {
	if err := preflightDirectHardening(config.SandboxConfig{ExecUID: 0, ExecGID: 0}); err == nil {
		t.Error("expected failure when exec uid/gid are unset")
	}

	if err := preflightDirectHardening(config.SandboxConfig{ExecUID: 65534, ExecGID: 0}); err == nil {
		t.Error("expected failure when exec gid is unset")
	}

	// Running Python as the server's own uid defeats the isolation and must be
	// rejected. Only meaningful when the runner is non-root.
	if self := os.Getuid(); self > 0 {
		err := preflightDirectHardening(config.SandboxConfig{ExecUID: self, ExecGID: self})
		if err == nil {
			t.Errorf("expected failure when exec uid equals server uid %d", self)
		}
	}
}

func TestPythonVenvRoot(t *testing.T) {
	cases := map[string]string{
		"/opt/panda-venv/bin/python": "/opt/panda-venv",
		"/usr/bin/python3":           "/usr",
		"/usr/local/bin/python":      "/usr/local",
		"/python":                    "/",
	}

	for in, want := range cases {
		if got := pythonVenvRoot(in); got != want {
			t.Errorf("pythonVenvRoot(%q) = %q, want %q", in, got, want)
		}
	}
}

// Params must ride the inherited pipe (ExtraFiles[0]), never the target env, so
// the untrusted Python environment cannot shadow a control value.
func TestHardenedSandboxCmdPassesParamsOnFD(t *testing.T) {
	targetEnv := []string{"ETHPANDAOPS_API_TOKEN=secret", "HOME=/work"}

	cmd, cleanup, err := newHardenedSandboxCmd(
		context.Background(), "/work", "/work/script.py", "/usr/bin/python3", 65534, 65534, targetEnv,
	)
	if err != nil {
		t.Fatalf("newHardenedSandboxCmd: %v", err)
	}
	defer cleanup()

	if len(cmd.ExtraFiles) != 1 {
		t.Fatalf("expected the params pipe as the sole ExtraFile, got %d", len(cmd.ExtraFiles))
	}

	// The child env is the target env verbatim — no __PANDA_SB_* control vars.
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "__PANDA_SB") {
			t.Errorf("control var leaked into the target env: %q", kv)
		}
	}

	// The params blob is readable from the pipe and round-trips.
	blob, err := io.ReadAll(cmd.ExtraFiles[0])
	if err != nil {
		t.Fatalf("reading params pipe: %v", err)
	}

	var p sandboxInitParams
	if err := json.Unmarshal(blob, &p); err != nil {
		t.Fatalf("decoding params: %v", err)
	}

	if p.UID != 65534 || p.GID != 65534 || p.WorkDir != "/work" || p.Script != "/work/script.py" {
		t.Errorf("params round-trip mismatch: %+v", p)
	}
}

func TestEffectiveCapsParses(t *testing.T) {
	if _, err := effectiveCaps(); err != nil {
		t.Errorf("effectiveCaps: %v", err)
	}
}

func TestLandlockABIVersion(t *testing.T) {
	if v := landlockABIVersion(); v < 1 {
		t.Skipf("landlock unavailable on this kernel (abi=%d)", v)
	}
}

// Each ABI must fold in exactly the rights it introduced (REFER@2, TRUNCATE@3,
// IOCTL_DEV@5) — an unhandled right is unrestricted — and no bit the kernel rejects.
func TestLandlockRightsHandlesEveryABIsSupportedRights(t *testing.T) {
	base := uint64(llAllABI1)
	refer := uint64(unix.LANDLOCK_ACCESS_FS_REFER)
	trunc := uint64(unix.LANDLOCK_ACCESS_FS_TRUNCATE)
	ioctl := uint64(unix.LANDLOCK_ACCESS_FS_IOCTL_DEV)

	cases := []struct {
		abi          int
		wantHandled  uint64
		wantTruncate uint64
		wantIoctlDev uint64
	}{
		{1, base, 0, 0},
		{2, base | refer, 0, 0},
		{3, base | refer | trunc, trunc, 0},
		{4, base | refer | trunc, trunc, 0},
		{5, base | refer | trunc | ioctl, trunc, ioctl},
		{6, base | refer | trunc | ioctl, trunc, ioctl},
	}

	for _, c := range cases {
		handled, truncate, ioctlDev := landlockRights(c.abi)
		if handled != c.wantHandled {
			t.Errorf("abi %d: handled = %#x, want %#x", c.abi, handled, c.wantHandled)
		}
		if truncate != c.wantTruncate {
			t.Errorf("abi %d: truncate = %#x, want %#x", c.abi, truncate, c.wantTruncate)
		}
		if ioctlDev != c.wantIoctlDev {
			t.Errorf("abi %d: ioctlDev = %#x, want %#x", c.abi, ioctlDev, c.wantIoctlDev)
		}
	}
}
