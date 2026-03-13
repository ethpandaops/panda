package cli

import (
	"net/http"
	"testing"

	"github.com/ethpandaops/panda/pkg/operations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDoraClientHelpers(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/operations/dora.list_networks", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(t, w, http.StatusOK, operations.NewObjectResponse(operations.DoraNetworksPayload{
			Networks: []operations.DoraNetwork{{Name: "hoodi", DoraURL: "https://dora.example"}},
		}, nil))
	})
	mux.HandleFunc("/api/v1/operations/dora.get_network_overview", func(w http.ResponseWriter, _ *http.Request) {
		payload := operations.DoraOverviewPayload{CurrentEpoch: 1, CurrentSlot: 32}
		writeJSONResponse(t, w, http.StatusOK, operations.NewObjectResponse(payload, nil))
	})
	for _, path := range []string{
		"/api/v1/operations/dora.get_validator",
		"/api/v1/operations/dora.get_slot",
		"/api/v1/operations/dora.get_epoch",
	} {
		mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`{"ok":true}`))
			require.NoError(t, err)
		})
	}

	newCLIHarness(t, mux)

	networks, err := listDoraNetworks()
	require.NoError(t, err)
	require.Len(t, networks, 1)
	assert.Equal(t, "hoodi", networks[0].Name)

	overview, err := doraOverview(operations.DoraNetworkArgs{Network: "hoodi"})
	require.NoError(t, err)
	assert.Equal(t, float64(1), overview.CurrentEpoch)

	validator, err := doraValidator(operations.DoraIndexOrPubkeyArgs{Network: "hoodi", IndexOrPubkey: "1"})
	require.NoError(t, err)
	assert.Equal(t, "application/json", validator.ContentType)

	slot, err := doraSlot(operations.DoraSlotOrHashArgs{Network: "hoodi", SlotOrHash: "head"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"ok":true}`, string(slot.Body))

	epoch, err := doraEpoch(operations.DoraEpochArgs{Network: "hoodi", Epoch: "1"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"ok":true}`, string(epoch.Body))
}
