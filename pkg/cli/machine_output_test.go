package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
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

// The documented format is a public contract: every always-JSON command must
// also render syntax failures as one JSON document without a flag.
func TestDocumentedJSONCommandsRenderSyntaxErrorsAsJSON(t *testing.T) {
	var visit func(*cobra.Command)
	visit = func(cmd *cobra.Command) {
		if strings.Contains(cmd.Short, "(always JSON)") {
			t.Run(cmd.CommandPath(), func(t *testing.T) {
				args := strings.Fields(cmd.CommandPath())[1:]
				stdout, stderr, code := runCLIProcess(t, args...)
				assert.Equal(t, 1, code)
				assert.Empty(t, stderr)
				var payload map[string]any
				require.NoError(t, json.Unmarshal([]byte(stdout), &payload))
				assert.NotEmpty(t, payload["error"])
			})
		}
		for _, child := range cmd.Commands() {
			visit(child)
		}
	}
	visit(rootCmd)
}

func TestJSONCommandResponsesAndUpstreamFailures(t *testing.T) {
	commands := [][]string{
		{"dora", "validator", "testnet", "1"},
		{"dora", "slot", "testnet", "1"},
		{"dora", "epoch", "testnet", "1"},
		{"ethnode", "beacon-get", "testnet", "lb", "/eth/v1/node/identity"},
		{"forky", "frame", "testnet", "1"},
		{"tracoor", "config", "testnet"},
		{"benchmarkoor", "run", "1"},
		{"benchmarkoor", "suite-stats", "1"},
		{"block-archive", "get", "testnet", "1", "0x123"},
		{"cbt", "transformation", "testnet", "model"},
		{"cbt", "intervals", "testnet"},
		{"cbt", "external", "testnet", "model"},
		{"cbt", "coverage", "testnet", "model", "--at", "0"},
	}
	for _, failure := range []bool{false, true} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if failure {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"upstream rejected the query"}`))
				return
			}
			_, _ = w.Write([]byte(`{"value":9007199254740993}`))
		}))
		config := filepath.Join(t.TempDir(), "config.yaml")
		require.NoError(t, os.WriteFile(config, []byte("server:\n  url: "+server.URL+"\n"), 0o600))
		for _, args := range commands {
			t.Run(fmt.Sprintf("%s/failure=%t", strings.Join(args, " "), failure), func(t *testing.T) {
				stdout, stderr, code := runCLIProcess(t, append([]string{"--config", config}, args...)...)
				assert.Empty(t, stderr)
				var payload map[string]any
				require.NoError(t, json.Unmarshal([]byte(stdout), &payload))
				if failure {
					assert.Equal(t, 1, code)
					assert.Contains(t, payload["error"], "upstream rejected the query")
				} else {
					assert.Zero(t, code)
					assert.Contains(t, stdout, "9007199254740993", "preserve upstream integer precision")
				}
			})
		}
		server.Close()
	}
}

func TestConditionalJSONOutputKeepsCBTListsAsText(t *testing.T) {
	for _, args := range [][]string{
		{"cbt", "external", "testnet"},
		{"cbt", "external", "testnet", "--database", "model"},
		{"cbt", "coverage", "testnet", "model", "--at", "-1"},
	} {
		// Fail before making a request; a list invocation keeps text errors.
		stdout, stderr, code := runCLIProcess(t, append(args, "--log-level", "invalid")...)
		assert.Equal(t, 1, code)
		assert.Empty(t, stdout)
		assert.Contains(t, stderr, "Error:")
	}
	for _, args := range [][]string{
		{"cbt", "external", "testnet", "model"},
		{"cbt", "coverage", "testnet", "model", "--at", "0"},
		{"cbt", "coverage", "testnet", "model", "--at", "invalid"},
	} {
		stdout, stderr, code := runCLIProcess(t, append(args, "--log-level", "invalid")...)
		assert.Equal(t, 1, code)
		assert.Empty(t, stderr)
		var payload map[string]any
		require.NoError(t, json.Unmarshal([]byte(stdout), &payload))
		assert.NotEmpty(t, payload["error"])
	}
}

func TestMachineOutputRejectsMalformedUpstreamJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html>upstream unavailable</html>`))
	}))
	defer server.Close()
	config := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(config, []byte("server:\n  url: "+server.URL+"\n"), 0o600))
	stdout, stderr, code := runCLIProcess(t, "--config", config, "dora", "validator", "testnet", "1")
	assert.Equal(t, 1, code)
	assert.Empty(t, stderr)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &payload))
	assert.Contains(t, payload["error"], "decoding JSON response")
}
