package sandbox

import (
	"os"
	"os/exec"
	"testing"

	"github.com/ethpandaops/panda/pkg/config"
)

// directExecTestUID is the id the tests drop the sandbox to — 65534 ("nobody"),
// distinct from any test-runner uid so the exec-uid≠server-uid invariant holds.
const directExecTestUID = 65534

// TestMain intercepts the direct-backend re-exec: the hardened Execute path
// re-execs this test binary, so the trampoline must be handled here.
func TestMain(m *testing.M) {
	RunDirectSandboxInitIfRequested()
	os.Exit(m.Run())
}

// requireDirectExec skips tests that run Python when the confinement stack is
// unavailable (no python3/caps/Landlock, or runner is uid 65534) — it fails closed.
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
