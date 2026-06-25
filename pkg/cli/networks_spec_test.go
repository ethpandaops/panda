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

func networkSpecTestServer(t *testing.T) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/operations/network.spec" {
			http.NotFound(w, r)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(operations.Response{
			Kind: operations.ResultKindObject,
			Data: map[string]any{
				"network":  "glamsterdam-devnet-5",
				"url":      "https://notes.ethereum.org/@ethpandaops/glamsterdam-devnet-5",
				"title":    "glamsterdam-devnet-5 spec",
				"markdown": "# glamsterdam-devnet-5 spec\n\n## EIP List\n\n...\n\n## Local testing\n\nKurtosis example:\n",
				"sections": []any{
					map[string]any{"heading": "EIP List", "content": "| EIP | Title |\n| --- | --- |"},
					map[string]any{"heading": "Local testing", "content": "Kurtosis example:\nnetwork_params:\n  gloas_fork_epoch: 1"},
				},
			},
		})
		require.NoError(t, err)
	}))
}

func runSpec(t *testing.T, args []string, list bool) string {
	t.Helper()

	server := networkSpecTestServer(t)
	t.Cleanup(server.Close)

	setClientConfig(t, server.URL)
	setOutputFormat(t, "text")

	return captureStdout(t, func() {
		cmd := testCommand()
		cmd.Flags().Bool("list", list, "")
		cmd.Flags().String("url", "", "")
		require.NoError(t, runNetworkSpec(cmd, args))
	})
}

func TestRunNetworkSpecFullMarkdown(t *testing.T) {
	out := runSpec(t, []string{"glamsterdam-devnet-5"}, false)

	assert.Contains(t, out, "# glamsterdam-devnet-5 spec")
	assert.Contains(t, out, "## Local testing")
}

func TestRunNetworkSpecListSections(t *testing.T) {
	out := runSpec(t, []string{"glamsterdam-devnet-5"}, true)

	assert.Contains(t, out, "glamsterdam-devnet-5 spec")
	assert.Contains(t, out, "- EIP List")
	assert.Contains(t, out, "- Local testing")
	assert.NotContains(t, out, "gloas_fork_epoch") // headings only, no content
}

func TestRunNetworkSpecSectionByHeading(t *testing.T) {
	out := runSpec(t, []string{"glamsterdam-devnet-5", "local"}, false)

	assert.Contains(t, out, "## Local testing")
	assert.Contains(t, out, "gloas_fork_epoch: 1")
	assert.NotContains(t, out, "EIP List")
}

func TestRunNetworkSpecSectionByContentFallback(t *testing.T) {
	// "kurtosis" matches no heading, but the Local testing body mentions it.
	out := runSpec(t, []string{"glamsterdam-devnet-5", "kurtosis"}, false)

	assert.Contains(t, out, "## Local testing")
	assert.Contains(t, out, "Kurtosis example:")
}

func TestRunNetworkSpecSectionNotFound(t *testing.T) {
	server := networkSpecTestServer(t)
	defer server.Close()

	setClientConfig(t, server.URL)
	setOutputFormat(t, "text")

	cmd := testCommand()
	cmd.Flags().Bool("list", false, "")
	cmd.Flags().String("url", "", "")

	err := runNetworkSpec(cmd, []string{"glamsterdam-devnet-5", "nonexistent-zzz"})
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "no section matching"))
}
