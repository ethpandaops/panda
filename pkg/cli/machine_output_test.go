package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Run the real entry point in a subprocess to observe Cobra's error output and
// process exit status, including failures before PersistentPreRunE runs.
func TestMachineOutputProcess(t *testing.T) {
	if os.Getenv("PANDA_TEST_CLI_PROCESS") != "1" {
		return
	}
	separator := slices.Index(os.Args, "--")
	require.GreaterOrEqual(t, separator, 0)
	os.Args = append([]string{"panda"}, os.Args[separator+1:]...)
	Execute()
	os.Exit(0)
}

func runCLIProcess(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], append([]string{"-test.run=^TestMachineOutputProcess$", "--"}, args...)...)
	cmd.Env = append(os.Environ(), "PANDA_TEST_CLI_PROCESS=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.String(), stderr.String(), 0
	}
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)
	require.NoError(t, ctx.Err())
	return stdout.String(), stderr.String(), exitErr.ExitCode()
}

func TestMachineOutputSyntaxErrors(t *testing.T) {
	for _, args := range [][]string{
		{"clickhouse", "query-raw"},
		{"clickhouse", "query-raw", "--bad"},
		{"datasources", "--json", "--bad"},
		{"datasources", "--bad", "--json"},
		{"search", "examples", "-o", "json"},
		{"execute", "--json", "--log-level", "bad", "-c", "print(1)"},
		{"--json", "no-such-command"},
		{"evm", "wallet"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stdout, stderr, code := runCLIProcess(t, args...)
			assert.Equal(t, 1, code)
			assert.Empty(t, stderr)
			var payload map[string]any
			require.NoError(t, json.Unmarshal([]byte(stdout), &payload))
			assert.NotEmpty(t, payload["error"])
		})
	}
}

func TestTextErrorsRetainUsage(t *testing.T) {
	for _, args := range [][]string{
		{"datasources", "--bad"},
		{"search", "examples"},
	} {
		stdout, stderr, code := runCLIProcess(t, args...)
		assert.Equal(t, 1, code)
		assert.Empty(t, stdout)
		assert.Contains(t, stderr, "Error:")
		assert.Contains(t, stderr, "Usage: panda")
	}
	// A flag-shaped code value must not turn a text invocation into JSON mode.
	stdout, stderr, code := runCLIProcess(t, "execute", "-c", "--json", "--log-level", "bad")
	assert.Equal(t, 1, code)
	assert.Empty(t, stdout)
	assert.Contains(t, stderr, "Error:")
}

func TestMachineOutputReportedFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/execute":
			_, _ = w.Write([]byte(`{"exit_code":7,"stdout":"","stderr":"execution failed"}`))
		case "/api/v1/build/trigger":
			_, _ = w.Write([]byte(`{"run_id":123,"client":"geth","run_url":"https://example.com/run/123"}`))
		case "/api/v1/build/status":
			_, _ = w.Write([]byte(`{"run_id":123,"status":"completed","conclusion":"failure"}`))
		case "/api/v1/operations/evm.faucet_start", "/api/v1/operations/evm.faucet_status":
			_, _ = w.Write([]byte(`{"kind":"object","data":{"job_id":"test-job","state":"failed","error":"claim rejected","terminal":true}}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"upstream rejected the query"}`))
		}
	}))
	defer server.Close()
	config := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(config, []byte("server:\n  url: "+server.URL+"\n"), 0o600))
	for _, tc := range []struct {
		name string
		args []string
		code int
		key  string
	}{
		{"execute", []string{"execute", "--json", "-c", "raise Exception()"}, 7, "exit_code"},
		{"build", []string{"build", "geth", "--wait", "--json"}, 1, "conclusion"},
		{"faucet", []string{"evm", "faucet", "testnet", "0x123"}, 1, "state"},
		{"query-raw", []string{"clickhouse", "query-raw", "test", "SELECT 1"}, 1, "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := runCLIProcess(t, append([]string{"--config", config}, tc.args...)...)
			assert.Equal(t, tc.code, code)
			assert.Empty(t, stderr)
			var payload map[string]any
			require.NoError(t, json.Unmarshal([]byte(stdout), &payload), "stdout must contain exactly one JSON document")
			assert.Contains(t, payload, tc.key)
		})
	}
}
