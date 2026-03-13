package cli

import (
	"net/http"
	"os"
	"testing"

	"github.com/ethpandaops/panda/pkg/serverapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveCodeRequiresExplicitSource(t *testing.T) {
	newCLIHarness(t, http.NewServeMux())

	originalStdin := os.Stdin
	devNull, err := os.Open("/dev/null")
	require.NoError(t, err)
	defer func() {
		_ = devNull.Close()
		os.Stdin = originalStdin
	}()

	os.Stdin = devNull
	executeCode = ""
	executeFile = ""

	_, err = resolveCode()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provide code via --code, --file, or stdin")
}

func TestRunExecuteFormatsUnknownSessionTTL(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/execute", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(t, w, http.StatusOK, serverapi.ExecuteResponse{
			Stdout:      "hello\n",
			SessionID:   "sess-1",
			OutputFiles: []string{"plot.png"},
		})
	})

	newCLIHarness(t, mux)
	executeCode = "print('hello')"

	stdout, stderr := captureOutput(t, func() {
		require.NoError(t, runExecute(executeCmd, nil))
	})
	assert.Equal(t, "hello\n", stdout)
	assert.Contains(t, stderr, "[files] plot.png")
	assert.Contains(t, stderr, "[session] sess-1 (ttl: unknown)")
}
