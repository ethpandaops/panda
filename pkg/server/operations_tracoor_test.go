package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethpandaops/cartographoor/pkg/discovery"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/operations"
)

func TestTracoorListArtifactsBuildsRequest(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{body: `{"beacon_states":[]}`, contentType: "application/json"}
	svc := newTracoorOperationService(transport)
	rec := httptest.NewRecorder()

	handled := svc.handleTracoorOperation("tracoor.list_artifacts", rec, newTracoorOpRequest(t, map[string]any{
		"network":               "testnet",
		"artifact":              "beacon_state",
		"node":                  "node-1",
		"slot":                  14535165,
		"beacon_implementation": "lighthouse",
	}))

	require.True(t, handled)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, transport.last)
	assert.Equal(t, "/v1/api/list-beacon-state", transport.last.URL.EscapedPath())

	var payload map[string]any
	require.NoError(t, json.Unmarshal(transport.lastBody, &payload))
	assert.Equal(t, "node-1", payload["node"])
	assert.Equal(t, float64(14535165), payload["slot"])
	assert.Equal(t, "lighthouse", payload["beacon_implementation"])

	pagination, ok := payload["pagination"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(defaultTracoorListLimit), pagination["limit"])
	assert.Equal(t, float64(0), pagination["offset"])
}

func TestTracoorListArtifactsRejectsUnknownArtifact(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{body: `{}`, contentType: "application/json"}
	svc := newTracoorOperationService(transport)
	rec := httptest.NewRecorder()

	handled := svc.handleTracoorOperation("tracoor.list_artifacts", rec, newTracoorOpRequest(t, map[string]any{
		"network":  "testnet",
		"artifact": "beacon_cake",
	}))

	require.True(t, handled)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Nil(t, transport.last)
}

// TestTracoorListArtifactsIgnoresUndeclaredFilters pins that a filter
// belonging to a different artifact type is not forwarded upstream.
func TestTracoorListArtifactsIgnoresUndeclaredFilters(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{body: `{"execution_block_traces":[]}`, contentType: "application/json"}
	svc := newTracoorOperationService(transport)
	rec := httptest.NewRecorder()

	handled := svc.handleTracoorOperation("tracoor.list_artifacts", rec, newTracoorOpRequest(t, map[string]any{
		"network":    "testnet",
		"artifact":   "execution_block_trace",
		"state_root": "0xabc",
		"slot":       123,
	}))

	require.True(t, handled)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "/v1/api/list-execution-block-trace", transport.last.URL.EscapedPath())

	var payload map[string]any
	require.NoError(t, json.Unmarshal(transport.lastBody, &payload))
	assert.NotContains(t, payload, "state_root")
	assert.NotContains(t, payload, "slot")
}

func TestTracoorCountArtifactsParsesStringCount(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{body: `{"count":"301406"}`, contentType: "application/json"}
	svc := newTracoorOperationService(transport)
	rec := httptest.NewRecorder()

	handled := svc.handleTracoorOperation("tracoor.count_artifacts", rec, newTracoorOpRequest(t, map[string]any{
		"network":  "testnet",
		"artifact": "beacon_block",
	}))

	require.True(t, handled)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "/v1/api/count-beacon-block", transport.last.URL.EscapedPath())

	var response operations.Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	data, ok := response.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(301406), data["count"])
	assert.Equal(t, "beacon_block", data["artifact"])

	// Count requests carry no pagination or id.
	var payload map[string]any
	require.NoError(t, json.Unmarshal(transport.lastBody, &payload))
	assert.NotContains(t, payload, "pagination")
	assert.NotContains(t, payload, "id")
}

func TestTracoorListUniqueValuesValidatesFields(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{body: `{}`, contentType: "application/json"}
	svc := newTracoorOperationService(transport)
	rec := httptest.NewRecorder()

	handled := svc.handleTracoorOperation("tracoor.list_unique_values", rec, newTracoorOpRequest(t, map[string]any{
		"network":  "testnet",
		"artifact": "beacon_state",
		"fields":   []any{"node", "block_extra_data"},
	}))

	require.True(t, handled)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Nil(t, transport.last)
}

func TestTracoorListUniqueValuesForwardsFields(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{body: `{"node":["a"]}`, contentType: "application/json"}
	svc := newTracoorOperationService(transport)
	rec := httptest.NewRecorder()

	handled := svc.handleTracoorOperation("tracoor.list_unique_values", rec, newTracoorOpRequest(t, map[string]any{
		"network":  "testnet",
		"artifact": "execution_bad_block",
		"fields":   []any{"node", "block_extra_data"},
	}))

	require.True(t, handled)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "/v1/api/list-unique-execution-bad-block-values", transport.last.URL.EscapedPath())

	var payload map[string]any
	require.NoError(t, json.Unmarshal(transport.lastBody, &payload))
	assert.Equal(t, []any{"node", "block_extra_data"}, payload["fields"])
}

func TestTracoorDownloadURLEscapesID(t *testing.T) {
	t.Parallel()

	svc := newTracoorOperationService(nil)
	rec := httptest.NewRecorder()

	handled := svc.handleTracoorOperation("tracoor.download_url", rec, newTracoorOpRequest(t, map[string]any{
		"network":  "testnet",
		"artifact": "beacon_state",
		"id":       "abc/def?x=1",
	}))

	require.True(t, handled)
	require.Equal(t, http.StatusOK, rec.Code)

	var response operations.Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	data, ok := response.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "https://tracoor.example.com/download/beacon_state/abc%2Fdef%3Fx=1", data["url"])
}

func TestTracoorLinkArtifact(t *testing.T) {
	t.Parallel()

	svc := newTracoorOperationService(nil)

	for name, testCase := range map[string]struct {
		args map[string]any
		url  string
	}{
		"listing": {
			args: map[string]any{"network": "testnet", "artifact": "beacon_bad_block"},
			url:  "https://tracoor.example.com/beacon_bad_block",
		},
		"single capture": {
			args: map[string]any{"network": "testnet", "artifact": "beacon_bad_block", "id": "some id"},
			url:  "https://tracoor.example.com/beacon_bad_block/some%20id",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			handled := svc.handleTracoorOperation("tracoor.link_artifact", rec, newTracoorOpRequest(t, testCase.args))

			require.True(t, handled)
			require.Equal(t, http.StatusOK, rec.Code)

			var response operations.Response
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
			data, ok := response.Data.(map[string]any)
			require.True(t, ok)
			assert.Equal(t, testCase.url, data["url"])
		})
	}
}

func TestTracoorUnknownNetwork(t *testing.T) {
	t.Parallel()

	svc := newTracoorOperationService(nil)
	rec := httptest.NewRecorder()

	handled := svc.handleTracoorOperation("tracoor.get_config", rec, newTracoorOpRequest(t, map[string]any{
		"network": "nope",
	}))

	require.True(t, handled)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestTracoorListNetworksSkipsNetworksWithoutTracoor(t *testing.T) {
	t.Parallel()

	log := logrus.New()
	log.SetOutput(io.Discard)

	svc := &service{
		log: log,
		cartographoorClient: tracoorOperationCartographoor{
			networks: map[string]discovery.Network{
				"testnet": {
					Name:        "testnet",
					Status:      "active",
					ServiceURLs: &discovery.ServiceURLs{Tracoor: "https://tracoor.example.com"},
				},
				"bare": {
					Name:   "bare",
					Status: "active",
				},
			},
		},
	}

	rec := httptest.NewRecorder()
	handled := svc.handleTracoorOperation("tracoor.list_networks", rec, nil)

	require.True(t, handled)
	require.Equal(t, http.StatusOK, rec.Code)

	var response operations.Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	data, ok := response.Data.(map[string]any)
	require.True(t, ok)
	networks, ok := data["networks"].([]any)
	require.True(t, ok)
	require.Len(t, networks, 1)

	network, ok := networks[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "testnet", network["name"])
	assert.Equal(t, "tracoor", network["type"])
}

func newTracoorOperationService(transport http.RoundTripper) *service {
	log := logrus.New()
	log.SetOutput(io.Discard)

	httpClient := http.DefaultClient
	if transport != nil {
		httpClient = &http.Client{Transport: transport}
	}

	return &service{
		log:        log,
		httpClient: httpClient,
		cartographoorClient: tracoorOperationCartographoor{
			networks: map[string]discovery.Network{
				"testnet": {
					Name:        "testnet",
					Status:      "active",
					ServiceURLs: &discovery.ServiceURLs{Tracoor: "https://tracoor.example.com"},
				},
			},
		},
	}
}

func newTracoorOpRequest(t *testing.T, args map[string]any) *http.Request {
	t.Helper()

	body, err := json.Marshal(operations.Request{Args: args})
	require.NoError(t, err)

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/runtime/operations/tracoor",
		io.NopCloser(bytes.NewReader(body)),
	)
	req.Header.Set("Content-Type", "application/json")

	return req
}

type tracoorOperationCartographoor struct {
	networks map[string]discovery.Network
}

func (c tracoorOperationCartographoor) Start(_ context.Context) error { return nil }
func (c tracoorOperationCartographoor) Stop() error                   { return nil }
func (c tracoorOperationCartographoor) GetAllNetworks() map[string]discovery.Network {
	return c.networks
}
func (c tracoorOperationCartographoor) GetActiveNetworks() map[string]discovery.Network {
	return c.networks
}
func (c tracoorOperationCartographoor) GetNetwork(name string) (discovery.Network, bool) {
	network, ok := c.networks[name]
	return network, ok
}
func (c tracoorOperationCartographoor) GetGroup(_ string) (map[string]discovery.Network, bool) {
	return nil, false
}
func (c tracoorOperationCartographoor) GetGroups() []string { return nil }
func (c tracoorOperationCartographoor) GetActiveGroups() map[string][]string {
	return nil
}
func (c tracoorOperationCartographoor) IsDevnet(_ discovery.Network) bool {
	return false
}
func (c tracoorOperationCartographoor) GetClusters(_ discovery.Network) []string {
	return nil
}
