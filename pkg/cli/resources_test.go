package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/serverapi"
)

func TestRunResourcesReadsDirectURI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/resources/read" {
			t.Errorf("path = %q, want /api/v1/resources/read", r.URL.Path)
			http.NotFound(w, r)
			return
		}

		if got := r.URL.Query().Get("uri"); got != "panda://getting-started" {
			t.Errorf("uri = %q, want panda://getting-started", got)
			http.Error(w, "bad uri", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "text/markdown")
		_, _ = fmt.Fprint(w, "guide content")
	}))
	defer server.Close()

	setClientConfig(t, server.URL)
	setOutputFormat(t, "text")

	output := captureStdout(t, func() {
		err := runResources(testCommand(), []string{"panda://getting-started"})
		require.NoError(t, err)
	})

	assert.Equal(t, "guide content", output)
}

func TestRunResourcesListsWithoutURI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/resources" {
			t.Errorf("path = %q, want /api/v1/resources", r.URL.Path)
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(serverapi.ListResourcesResponse{
			Resources: []serverapi.ResourceInfo{{
				URI:         "panda://getting-started",
				Name:        "Getting Started",
				Description: "Start here",
			}},
		}); err != nil {
			t.Errorf("encoding response: %v", err)
		}
	}))
	defer server.Close()

	setClientConfig(t, server.URL)
	setOutputFormat(t, "text")

	output := captureStdout(t, func() {
		err := runResources(testCommand(), nil)
		require.NoError(t, err)
	})

	assert.Contains(t, output, "Resources:")
	assert.Contains(t, output, "panda://getting-started")
}

func TestResourcesReadHasGetAliasAndListSubcommand(t *testing.T) {
	assert.Contains(t, resourcesReadCmd.Aliases, "get")

	var foundList bool
	for _, cmd := range resourcesCmd.Commands() {
		if cmd.Name() == "list" {
			foundList = true
			break
		}
	}

	assert.True(t, foundList)
}

func TestServerErrorHintUsesExistingResourcesCommand(t *testing.T) {
	hint := serverErrorHint(http.StatusNotFound, "missing")

	assert.Contains(t, hint, "panda resources")
	assert.NotContains(t, hint, "panda resources list")
}

func setOutputFormat(t *testing.T, value string) {
	t.Helper()

	original := outputFormat
	outputFormat = value
	t.Cleanup(func() { outputFormat = original })
}

func testCommand() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	return cmd
}
