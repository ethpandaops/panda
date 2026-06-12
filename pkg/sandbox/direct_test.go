package sandbox

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/config"
)

// TestDirectBackendWithholdsProcessSecrets is the regression gate for the
// credential leak: the direct backend must NOT pass the panda-server process
// env (which holds PANDA_BOT_TOKEN) to untrusted, LLM-generated code. The data
// plane is reached via req.Env, so a secret living only in the process env must
// be invisible to the executed script, while req.Env stays visible.
func TestDirectBackendWithholdsProcessSecrets(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	t.Setenv("PANDA_BOT_TOKEN", "super-secret-bot-token")

	b, err := NewDirectBackend(config.SandboxConfig{Timeout: 30}, logrus.New())
	if err != nil {
		t.Fatalf("NewDirectBackend: %v", err)
	}

	res, err := b.Execute(context.Background(), ExecuteRequest{
		Code: "import os\n" +
			"print('BOT=' + os.environ.get('PANDA_BOT_TOKEN', 'ABSENT'))\n" +
			"print('REQ=' + os.environ.get('FROM_REQ', 'ABSENT'))\n",
		Env: map[string]string{"FROM_REQ": "visible"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if strings.Contains(res.Stdout, "super-secret-bot-token") {
		t.Fatalf("bot token leaked into executed code: %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "BOT=ABSENT") {
		t.Errorf("expected PANDA_BOT_TOKEN withheld (BOT=ABSENT), got: %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "REQ=visible") {
		t.Errorf("expected req.Env passthrough (REQ=visible), got: %q", res.Stdout)
	}
}
