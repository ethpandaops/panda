package cli

import (
	"net/http"
	"testing"

	"github.com/ethpandaops/panda/pkg/operations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEthnodeClientHelpers(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/operations/ethnode.get_node_syncing", func(w http.ResponseWriter, _ *http.Request) {
		payload := operations.EthNodeSyncingPayload{}
		payload.Data.HeadSlot = "1"
		writeJSONResponse(t, w, http.StatusOK, operations.NewObjectResponse(payload, nil))
	})
	mux.HandleFunc("/api/v1/operations/ethnode.get_node_version", func(w http.ResponseWriter, _ *http.Request) {
		payload := operations.EthNodeVersionPayload{}
		payload.Data.Version = "Lighthouse/v1"
		writeJSONResponse(t, w, http.StatusOK, operations.NewObjectResponse(payload, nil))
	})
	mux.HandleFunc("/api/v1/operations/ethnode.web3_client_version", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(t, w, http.StatusOK, operations.NewObjectResponse("Geth/v1", nil))
	})
	mux.HandleFunc("/api/v1/operations/ethnode.get_node_health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(t, w, http.StatusOK, operations.NewObjectResponse(operations.StatusCodePayload{StatusCode: 200}, nil))
	})
	mux.HandleFunc("/api/v1/operations/ethnode.get_peer_count", func(w http.ResponseWriter, _ *http.Request) {
		payload := operations.EthNodePeerCountPayload{}
		payload.Data.Connected = "5"
		writeJSONResponse(t, w, http.StatusOK, operations.NewObjectResponse(payload, nil))
	})
	mux.HandleFunc("/api/v1/operations/ethnode.get_finality_checkpoints", func(w http.ResponseWriter, _ *http.Request) {
		payload := operations.EthNodeFinalityPayload{}
		payload.Data.Finalized.Epoch = "10"
		writeJSONResponse(t, w, http.StatusOK, operations.NewObjectResponse(payload, nil))
	})
	mux.HandleFunc("/api/v1/operations/ethnode.get_beacon_headers", func(w http.ResponseWriter, _ *http.Request) {
		payload := operations.EthNodeHeaderPayload{}
		payload.Data.Root = "0xabc"
		writeJSONResponse(t, w, http.StatusOK, operations.NewObjectResponse(payload, nil))
	})
	mux.HandleFunc("/api/v1/operations/ethnode.eth_block_number", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(t, w, http.StatusOK, operations.NewObjectResponse(operations.EthNodeBlockNumberPayload{BlockNumber: 42}, nil))
	})
	for _, path := range []string{
		"/api/v1/operations/ethnode.beacon_get",
		"/api/v1/operations/ethnode.execution_rpc",
	} {
		mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`{"ok":true}`))
			require.NoError(t, err)
		})
	}

	newCLIHarness(t, mux)

	syncing, err := ethNodeSyncing(operations.EthNodeNodeArgs{Network: "hoodi", Instance: "node"})
	require.NoError(t, err)
	assert.Equal(t, "1", syncing.Data.HeadSlot)

	version, err := ethNodeVersion(operations.EthNodeNodeArgs{Network: "hoodi", Instance: "node"})
	require.NoError(t, err)
	assert.Equal(t, "Lighthouse/v1", version.Data.Version)

	executionVersion, err := ethNodeExecutionClientVersion(operations.EthNodeNodeArgs{Network: "hoodi", Instance: "node"})
	require.NoError(t, err)
	assert.Equal(t, "Geth/v1", executionVersion)

	health, err := ethNodeHealth(operations.EthNodeNodeArgs{Network: "hoodi", Instance: "node"})
	require.NoError(t, err)
	assert.Equal(t, 200, health.StatusCode)

	peers, err := ethNodePeerCount(operations.EthNodeNodeArgs{Network: "hoodi", Instance: "node"})
	require.NoError(t, err)
	assert.Equal(t, "5", peers.Data.Connected)

	finality, err := ethNodeFinality(operations.EthNodeFinalityArgs{Network: "hoodi", Instance: "node"})
	require.NoError(t, err)
	assert.Equal(t, "10", finality.Data.Finalized.Epoch)

	headers, err := ethNodeHeaders(operations.EthNodeBeaconHeadersArgs{Network: "hoodi", Instance: "node", Slot: "head"})
	require.NoError(t, err)
	assert.Equal(t, "0xabc", headers.Data.Root)

	blockNumber, err := ethNodeBlockNumber(operations.EthNodeNodeArgs{Network: "hoodi", Instance: "node"})
	require.NoError(t, err)
	assert.Equal(t, uint64(42), blockNumber.BlockNumber)

	beaconGet, err := ethNodeBeaconGet(operations.EthNodeBeaconGetArgs{Network: "hoodi", Instance: "node", Path: "/eth/v1/node/identity"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"ok":true}`, string(beaconGet.Body))

	executionRPC, err := ethNodeExecutionRPC(operations.EthNodeExecutionRPCArgs{Network: "hoodi", Instance: "node", Method: "eth_chainId"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"ok":true}`, string(executionRPC.Body))
}
