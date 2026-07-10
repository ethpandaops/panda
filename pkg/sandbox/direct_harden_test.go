package sandbox

import (
	"os"
	"os/exec"
	"testing"

	"github.com/ethpandaops/panda/pkg/config"
)

// directExecTestUID is the unprivileged id the tests drop the sandbox to. 65534
// ("nobody" on Debian/Alpine) is distinct from any plausible test-runner uid, so
// the exec-uid-differs-from-server-uid invariant holds in CI.
const directExecTestUID = 65534

// TestMain intercepts the direct-backend re-exec. The hardened Execute path
// re-execs /proc/self/exe — which, under `go test`, is this test binary — so the
// trampoline must be handled here or execution could never complete. A normal
// test run is a no-op and proceeds to the suite.
func TestMain(m *testing.M) {
	RunDirectSandboxInitIfRequested()
	os.Exit(m.Run())
}

// requireDirectExec skips a test that actually runs Python when this environment
// cannot provide the direct backend's confinement (no python3, no CAP_SYS_ADMIN/
// SETUID/SETGID, no Landlock, or the runner is uid 65534 itself). Execution is
// fail-closed, so there is nothing to exercise without the full stack.
func requireDirectExec(t *testing.T) {
	t.Helper()

	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	probe := config.SandboxConfig{ExecUID: directExecTestUID, ExecGID: directExecTestUID}
	if err := preflightDirectHardening(probe); err != nil {
		t.Skipf("direct-backend confinement unavailable in this environment: %v", err)
	}
}
