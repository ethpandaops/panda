package cli

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/ethpandaops/panda/pkg/serverapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSearchCommandHelpAndNoResults(t *testing.T) {
	var output bytes.Buffer
	searchCmd.SetOut(&output)
	t.Cleanup(func() {
		searchCmd.SetOut(nil)
	})

	require.NoError(t, searchCmd.RunE(searchCmd, nil))
	assert.Contains(t, output.String(), "Semantic search")

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/search/examples", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(t, w, http.StatusOK, serverapi.SearchExamplesResponse{})
	})
	mux.HandleFunc("/api/v1/search/runbooks", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(t, w, http.StatusOK, serverapi.SearchRunbooksResponse{})
	})

	newCLIHarness(t, mux)

	stdout, _ := captureOutput(t, func() {
		require.NoError(t, runSearchExamples(searchExamplesCmd, []string{"missing"}))
	})
	assert.Contains(t, stdout, "No matching examples found.")

	stdout, _ = captureOutput(t, func() {
		require.NoError(t, runSearchRunbooks(searchRunbooksCmd, []string{"missing"}))
	})
	assert.Contains(t, stdout, "No matching runbooks found.")
}

func TestRunSearchRunbooksFormatsResults(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/search/runbooks", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(t, w, http.StatusOK, serverapi.SearchRunbooksResponse{
			Results: []*serverapi.SearchRunbookResult{
				{Name: "Finality lag", Description: "Investigate lag", Tags: []string{"finality"}, Content: "Step 1"},
			},
		})
	})

	newCLIHarness(t, mux)

	stdout, _ := captureOutput(t, func() {
		require.NoError(t, runSearchRunbooks(searchRunbooksCmd, []string{"finality"}))
	})
	assert.Contains(t, stdout, "Finality lag")
	assert.Contains(t, stdout, "Tags: finality")
	assert.NotContains(t, stdout, "Prerequisites:")
}
