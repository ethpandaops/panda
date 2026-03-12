package cli

import (
	"net/http"
	"testing"

	"github.com/ethpandaops/panda/pkg/operations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEthNodeVersionJSONIncludesExecutionErrors(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/operations/ethnode.get_node_version", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(t, w, http.StatusOK, operations.NewObjectResponse(operations.EthNodeVersionPayload{
			Data: struct {
				Version string `json:"version"`
			}{Version: "Lighthouse/v1.0.0"},
		}, nil))
	})
	mux.HandleFunc("/api/v1/operations/ethnode.web3_client_version", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(t, w, http.StatusBadGateway, map[string]string{"error": "execution unavailable"})
	})

	newCLIHarness(t, mux)
	ethnodeJSON = true

	stdout, _ := captureOutput(t, func() {
		require.NoError(t, ethNodeVersionCmd.RunE(ethNodeVersionCmd, []string{"hoodi", "node-1"}))
	})
	assert.Contains(t, stdout, `"beacon"`)
	assert.Contains(t, stdout, `"execution_error": "HTTP 502: execution unavailable"`)
}

func TestEthNodeBeaconAndExecRPCCommandsHandleFormatting(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/operations/ethnode.beacon_get", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"path":"/eth/v1/node/identity"}`))
		require.NoError(t, err)
	})
	mux.HandleFunc("/api/v1/operations/ethnode.execution_rpc", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"result":"0x1"}`))
		require.NoError(t, err)
	})

	newCLIHarness(t, mux)

	stdout, _ := captureOutput(t, func() {
		require.NoError(t, ethNodeBeaconGetCmd.RunE(ethNodeBeaconGetCmd, []string{"hoodi", "node-1", "eth/v1/node/identity"}))
	})
	assert.Contains(t, stdout, `/eth/v1/node/identity`)

	stdout, _ = captureOutput(t, func() {
		require.NoError(t, ethNodeExecRPCCmd.RunE(ethNodeExecRPCCmd, []string{"hoodi", "node-1", "eth_chainId"}))
	})
	assert.Contains(t, stdout, `"0x1"`)

	err := ethNodeExecRPCCmd.RunE(ethNodeExecRPCCmd, []string{"hoodi", "node-1", "eth_getBlockByNumber", `{"bad":"json"}`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid params JSON")
}
