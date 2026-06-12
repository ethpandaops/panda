package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunNetworksReadsActiveNetworksResource(t *testing.T) {
	server := activeNetworksTestServer(t)
	defer server.Close()

	setClientConfig(t, server.URL)
	setOutputFormat(t, "text")
	setNetworksDevnetsOnly(t, false)

	output := captureStdout(t, func() {
		err := runNetworks(testCommand(), nil)
		require.NoError(t, err)
	})

	assert.Contains(t, output, "ID")
	assert.Contains(t, output, "DEVNET_GROUP")
	assert.Contains(t, output, "mainnet")
	assert.Contains(t, output, "alpha-devnet-1")
	assert.Contains(t, output, "Source: networks://active")
}

func TestRunDevnetsFiltersActiveNetworks(t *testing.T) {
	server := activeNetworksTestServer(t)
	defer server.Close()

	setClientConfig(t, server.URL)
	setOutputFormat(t, "text")

	output := captureStdout(t, func() {
		err := runDevnets(testCommand(), nil)
		require.NoError(t, err)
	})

	assert.Contains(t, output, "alpha-devnet-1")
	assert.Contains(t, output, "alpha")
	assert.NotContains(t, output, "mainnet")
}

func TestDevnetCommandAliasHasListSubcommand(t *testing.T) {
	assert.Contains(t, devnetsCmd.Aliases, "devnet")

	var found bool
	for _, cmd := range devnetsCmd.Commands() {
		if cmd.Name() == "list" {
			found = true
			break
		}
	}

	assert.True(t, found)
}

func activeNetworksTestServer(t *testing.T) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/resources/read" {
			http.NotFound(w, r)
			return
		}

		assert.Equal(t, "networks://active", r.URL.Query().Get("uri"))
		assert.Equal(t, "cli", r.URL.Query().Get("client_context"))

		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(activeNetworksResponse{
			Networks: []activeNetworkSummary{
				{
					ID:          "mainnet",
					Name:        "mainnet",
					ChainID:     1,
					Status:      "active",
					IsDevnet:    false,
					ResourceURI: "networks://mainnet",
				},
				{
					ID:          "alpha-devnet-1",
					Name:        "devnet-1",
					ChainID:     901,
					Status:      "active",
					IsDevnet:    true,
					DevnetGroup: "alpha",
					ResourceURI: "networks://alpha-devnet-1",
				},
			},
			Groups:             []string{"alpha"},
			ActiveDevnetGroups: map[string][]string{"alpha": {"alpha-devnet-1"}},
			Usage:              "test payload",
		})
		require.NoError(t, err)
	}))
}

func setNetworksDevnetsOnly(t *testing.T, value bool) {
	t.Helper()

	original := networksDevnetsOnly
	networksDevnetsOnly = value
	t.Cleanup(func() { networksDevnetsOnly = original })
}
