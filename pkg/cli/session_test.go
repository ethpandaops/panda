package cli

import (
	"net/http"
	"testing"

	"github.com/ethpandaops/panda/pkg/serverapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunSessionListShowsNoSessions(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		writeJSONResponse(t, w, http.StatusOK, serverapi.ListSessionsResponse{})
	})

	newCLIHarness(t, mux)

	stdout, _ := captureOutput(t, func() {
		require.NoError(t, runSessionList(sessionListCmd, nil))
	})
	assert.Contains(t, stdout, "No active sessions.")
}

func TestRunSessionDestroySuppressesTextInJSONMode(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/sessions/sess-1", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodDelete, r.Method)
		w.WriteHeader(http.StatusNoContent)
	})

	newCLIHarness(t, mux)
	sessionJSON = true

	stdout, _ := captureOutput(t, func() {
		require.NoError(t, runSessionDestroy(sessionDestroyCmd, []string{"sess-1"}))
	})
	assert.Empty(t, stdout)
}
