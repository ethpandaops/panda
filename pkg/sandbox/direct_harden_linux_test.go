//go:build linux

package sandbox

import (
	"os"
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

func TestStrippedEnvRemovesControlVars(t *testing.T) {
	t.Setenv(envCtlUID, "65534")
	t.Setenv(envCtlScript, "/work/script.py")
	t.Setenv("KEEP_ME", "yes")

	for _, kv := range strippedEnv() {
		if len(kv) >= len(envCtlUID) && kv[:len(envCtlUID)] == envCtlUID {
			t.Errorf("control var leaked into python env: %q", kv)
		}
	}

	var kept bool
	for _, kv := range strippedEnv() {
		if kv == "KEEP_ME=yes" {
			kept = true
		}
	}
	if !kept {
		t.Error("strippedEnv dropped a non-control variable")
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

// TestLandlockRightsHandlesEveryABIsSupportedRights guards against the confinement
// gap where a right the ruleset does not handle is left unrestricted: each ABI
// must fold in exactly the rights it introduced (REFER@2, TRUNCATE@3, IOCTL_DEV@5)
// and no bit the kernel would reject.
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
