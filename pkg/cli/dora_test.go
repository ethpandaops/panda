package cli

import (
	"net/http"
	"testing"

	"github.com/ethpandaops/panda/pkg/operations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDoraNetworksAndOverviewPrintFriendlyOutput(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/operations/dora.list_networks", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(t, w, http.StatusOK, operations.NewObjectResponse(operations.DoraNetworksPayload{
			Networks: []operations.DoraNetwork{
				{Name: "hoodi", DoraURL: "https://hoodi.example"},
			},
		}, nil))
	})
	mux.HandleFunc("/api/v1/operations/dora.get_network_overview", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(t, w, http.StatusOK, operations.NewObjectResponse(operations.DoraOverviewPayload{
			CurrentEpoch:      123,
			CurrentSlot:       456,
			Finalized:         true,
			ParticipationRate: 0.99,
		}, nil))
	})

	newCLIHarness(t, mux)

	stdout, _ := captureOutput(t, func() {
		require.NoError(t, doraNetworksCmd.RunE(doraNetworksCmd, nil))
	})
	assert.Contains(t, stdout, "hoodi")
	assert.Contains(t, stdout, "https://hoodi.example")

	stdout, _ = captureOutput(t, func() {
		require.NoError(t, doraOverviewCmd.RunE(doraOverviewCmd, []string{"hoodi"}))
	})
	assert.Regexp(t, `Network:\s+hoodi`, stdout)
	assert.Regexp(t, `Current epoch:\s+123`, stdout)
	assert.NotContains(t, stdout, "Active validators:")
}

func TestDoraCommandsPrintJSONBodies(t *testing.T) {
	mux := http.NewServeMux()
	rawResponse := []byte(`{"ok":true,"network":"hoodi"}`)
	mux.HandleFunc("/api/v1/operations/dora.get_validator", func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write(rawResponse)
		require.NoError(t, err)
	})
	mux.HandleFunc("/api/v1/operations/dora.get_slot", func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write(rawResponse)
		require.NoError(t, err)
	})
	mux.HandleFunc("/api/v1/operations/dora.get_epoch", func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write(rawResponse)
		require.NoError(t, err)
	})

	newCLIHarness(t, mux)

	stdout, _ := captureOutput(t, func() {
		require.NoError(t, doraValidatorCmd.RunE(doraValidatorCmd, []string{"hoodi", "123"}))
	})
	assert.Contains(t, stdout, `"network": "hoodi"`)

	stdout, _ = captureOutput(t, func() {
		require.NoError(t, doraSlotCmd.RunE(doraSlotCmd, []string{"hoodi", "456"}))
	})
	assert.Contains(t, stdout, `"ok": true`)

	stdout, _ = captureOutput(t, func() {
		require.NoError(t, doraEpochCmd.RunE(doraEpochCmd, []string{"hoodi", "7"}))
	})
	assert.Contains(t, stdout, `"network": "hoodi"`)
}
