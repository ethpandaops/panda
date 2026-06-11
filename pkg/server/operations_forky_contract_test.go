package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ethpandaops/forky/api/rest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests decode the request bodies panda builds with Forky's own
// ogen-generated spec types. The runtime path stays loosely typed on purpose
// (drift tolerance across mixed forky versions); these catch panda drifting
// from the spec when the forky dependency is bumped.

func TestForkyMetadataQueryMatchesSpec(t *testing.T) {
	t.Parallel()

	query, err := forkyMetadataQuery(map[string]any{
		"network":          "testnet",
		"node":             "node-1",
		"slot":             7654321,
		"epoch":            239197,
		"labels":           []any{"reorg", "mainnet"},
		"consensus_client": "lighthouse",
		"event_source":     "xatu_reorg_event",
		"before":           "2026-06-11T00:00:00Z",
		"after":            "2026-06-10T00:00:00Z",
		"offset":           5,
		"limit":            10,
	})
	require.NoError(t, err)

	body, err := json.Marshal(query)
	require.NoError(t, err)

	var decoded rest.MetadataQuery
	require.NoError(t, decoded.UnmarshalJSON(body), "panda request body must decode as a spec MetadataQuery")
	require.NoError(t, decoded.Validate())

	filter := decoded.GetFilter().Value
	assert.Equal(t, "node-1", filter.GetNode().Value)
	assert.Equal(t, rest.Slot(7654321), filter.GetSlot().Value)
	assert.Equal(t, rest.Epoch(239197), filter.GetEpoch().Value)
	assert.Equal(t, []string{"reorg", "mainnet"}, filter.GetLabels())
	assert.Equal(t, "lighthouse", filter.GetConsensusClient().Value)
	assert.Equal(t, "xatu_reorg_event", filter.GetEventSource().Value)
	assert.Equal(t, time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC), filter.GetBefore().Value)
	assert.Equal(t, time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC), filter.GetAfter().Value)

	pagination := decoded.GetPagination().Value
	assert.Equal(t, 5, pagination.GetOffset().Value)
	assert.Equal(t, 10, pagination.GetLimit().Value)
}

func TestForkyMetadataQueryDefaultsMatchSpec(t *testing.T) {
	t.Parallel()

	query, err := forkyMetadataQuery(map[string]any{"network": "testnet"})
	require.NoError(t, err)

	body, err := json.Marshal(query)
	require.NoError(t, err)

	var decoded rest.MetadataQuery
	require.NoError(t, decoded.UnmarshalJSON(body))
	require.NoError(t, decoded.Validate())

	pagination := decoded.GetPagination().Value
	assert.Equal(t, 0, pagination.GetOffset().Value)
	assert.Equal(t, defaultForkyListLimit, pagination.GetLimit().Value)
}

// TestForkyResponseUnwrapMatchesSpec encodes a response with the generated
// spec types and asserts panda's envelope unwrapping reads it correctly.
func TestForkyResponseUnwrapMatchesSpec(t *testing.T) {
	t.Parallel()

	response := rest.ListMetadataOK{
		Data: rest.ListMetadataOKData{
			Frames: []rest.FrameMetadata{{
				ID:              "0d2855c9-cf83-4b0a-9d83-041f25d39bd5",
				Node:            "node-1",
				FetchedAt:       time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC),
				WallClockSlot:   rest.Slot(7654321),
				WallClockEpoch:  rest.Epoch(239197),
				Labels:          []string{"reorg"},
				ConsensusClient: "lighthouse",
				EventSource:     "xatu_reorg_event",
			}},
			Pagination: rest.PaginationResponse{Total: 42},
		},
	}

	body, err := response.MarshalJSON()
	require.NoError(t, err)

	data, err := forkyDataObject(body)
	require.NoError(t, err)

	frames, ok := data["frames"].([]any)
	require.True(t, ok)
	require.Len(t, frames, 1)

	frame, ok := frames[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "node-1", frame["node"])
	assert.Equal(t, float64(7654321), frame["wall_clock_slot"])

	pagination, ok := data["pagination"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(42), pagination["total"])
}
