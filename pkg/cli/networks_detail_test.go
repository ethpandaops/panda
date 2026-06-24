package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/operations"
)

func networkGetTestServer(t *testing.T) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/operations/network.get" {
			http.NotFound(w, r)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(operations.Response{
			Kind: operations.ResultKindObject,
			Data: map[string]any{
				"id":           "fusaka-devnet-3",
				"name":         "fusaka-devnet-3",
				"status":       "active",
				"chain_id":     7088110746,
				"is_devnet":    true,
				"devnet_group": "fusaka",
				"genesis_time": 1700000000,
				"forks": map[string]any{
					"consensus": map[string]any{
						"deneb":   map[string]any{"epoch": 0},
						"electra": map[string]any{"epoch": 10, "timestamp": 1700001000},
					},
					"execution": map[string]any{
						"prague": map[string]any{"block": 0},
					},
				},
				"clients": []any{
					map[string]any{"name": "geth", "version": "v1.15.0"},
					map[string]any{"name": "lighthouse", "version": "v6.0.0"},
				},
				"endpoints": map[string]any{
					"rpc":    "https://rpc.fusaka.example.com",
					"beacon": "https://beacon.fusaka.example.com",
					"dora":   "https://dora.fusaka.example.com",
				},
				"node_inventory_url": "https://config.fusaka.example.com/api/v1/nodes/inventory",
			},
		})
		require.NoError(t, err)
	}))
}

func TestRunNetworkInfoRendersDetail(t *testing.T) {
	server := networkGetTestServer(t)
	defer server.Close()

	setClientConfig(t, server.URL)
	setOutputFormat(t, "text")

	output := captureStdout(t, func() {
		err := runNetworkInfo(testCommand(), []string{"fusaka-devnet-3"})
		require.NoError(t, err)
	})

	assert.Contains(t, output, "fusaka-devnet-3")
	assert.Contains(t, output, "Chain ID")
	assert.Contains(t, output, "7088110746")
	assert.Contains(t, output, "Forks:")
	assert.Contains(t, output, "electra")
	assert.Contains(t, output, "Clients:")
	assert.Contains(t, output, "geth")
	assert.Contains(t, output, "Endpoints:")
	assert.Contains(t, output, "https://rpc.fusaka.example.com")
}

func TestRunNetworkForksOrdersByActivation(t *testing.T) {
	server := networkGetTestServer(t)
	defer server.Close()

	setClientConfig(t, server.URL)
	setOutputFormat(t, "text")

	output := captureStdout(t, func() {
		err := runNetworkForks(testCommand(), []string{"fusaka-devnet-3"})
		require.NoError(t, err)
	})

	denebAt := strings.Index(output, "deneb")
	electraAt := strings.Index(output, "electra")
	require.GreaterOrEqual(t, denebAt, 0)
	require.GreaterOrEqual(t, electraAt, 0)
	assert.Less(t, denebAt, electraAt, "deneb (epoch 0) should sort before electra (epoch 10)")
}

func TestRunNetworkEndpointsLists(t *testing.T) {
	server := networkGetTestServer(t)
	defer server.Close()

	setClientConfig(t, server.URL)
	setOutputFormat(t, "text")

	output := captureStdout(t, func() {
		err := runNetworkEndpoints(testCommand(), []string{"fusaka-devnet-3"})
		require.NoError(t, err)
	})

	assert.Contains(t, output, "beacon")
	assert.Contains(t, output, "https://beacon.fusaka.example.com")
	assert.Contains(t, output, "dora")
}
