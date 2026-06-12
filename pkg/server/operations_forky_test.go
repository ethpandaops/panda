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

func TestForkyGetNowUnwrapsDataEnvelope(t *testing.T) {
	t.Parallel()

	forkyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/ethereum/now":
			assert.Equal(t, http.MethodGet, r.Method)
			_, _ = w.Write([]byte(`{"data":{"slot":7654321,"epoch":239197}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(forkyServer.Close)

	svc := newForkyOperationService(forkyServer.Client(), forkyServer.URL)
	rec := httptest.NewRecorder()

	handled := svc.handleForkyOperation("forky.get_now", rec, newForkyOpRequest(t, map[string]any{"network": "testnet"}))
	require.True(t, handled)
	require.Equal(t, http.StatusOK, rec.Code)

	var response operations.Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))

	data, ok := response.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(7654321), data["slot"])
	assert.Equal(t, float64(239197), data["epoch"])
}

func TestForkyListFramesBuildsMetadataQuery(t *testing.T) {
	t.Parallel()

	forkyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/metadata":
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

			var query map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&query))

			filter, ok := query["filter"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, "node-1", filter["node"])
			assert.Equal(t, float64(7654321), filter["slot"])
			assert.Equal(t, "lighthouse", filter["consensus_client"])
			assert.Equal(t, "xatu_reorg_event", filter["event_source"])
			assert.Equal(t, []any{"reorg"}, filter["labels"])
			assert.NotContains(t, filter, "epoch")

			pagination, ok := query["pagination"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, float64(5), pagination["offset"])
			assert.Equal(t, float64(10), pagination["limit"])

			_, _ = w.Write([]byte(`{"data":{"frames":[{"id":"abc"}],"pagination":{"total":42}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(forkyServer.Close)

	svc := newForkyOperationService(forkyServer.Client(), forkyServer.URL)
	rec := httptest.NewRecorder()

	handled := svc.handleForkyOperation("forky.list_frames", rec, newForkyOpRequest(t, map[string]any{
		"network":          "testnet",
		"node":             "node-1",
		"slot":             7654321,
		"labels":           []any{"reorg"},
		"consensus_client": "lighthouse",
		"event_source":     "xatu_reorg_event",
		"offset":           5,
		"limit":            10,
	}))
	require.True(t, handled)
	require.Equal(t, http.StatusOK, rec.Code)

	var response operations.Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))

	data, ok := response.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(42), data["total"])

	frames, ok := data["frames"].([]any)
	require.True(t, ok)
	require.Len(t, frames, 1)
}

func TestForkyGetFramePassthroughEscapesIdentifier(t *testing.T) {
	t.Parallel()

	forkyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/frames/abc%2Fdef%3Fx=1", r.URL.EscapedPath())
		assert.Empty(t, r.URL.RawQuery)
		_, _ = w.Write([]byte(`{"data":{"frame":{"metadata":{"id":"escaped"}}}}`))
	}))
	t.Cleanup(forkyServer.Close)

	svc := newForkyOperationService(forkyServer.Client(), forkyServer.URL)
	rec := httptest.NewRecorder()

	handled := svc.handleForkyOperation("forky.get_frame", rec, newForkyOpRequest(t, map[string]any{
		"network":  "testnet",
		"frame_id": "abc/def?x=1",
	}))
	require.True(t, handled)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"id":"escaped"`)
}

func TestForkyLinkFrame(t *testing.T) {
	t.Parallel()

	svc := newForkyOperationService(http.DefaultClient, "https://forky.testnet.example.com/")
	rec := httptest.NewRecorder()

	handled := svc.handleForkyOperation("forky.link_frame", rec, newForkyOpRequest(t, map[string]any{
		"network":  "testnet",
		"frame_id": "0d2855c9",
	}))
	require.True(t, handled)
	require.Equal(t, http.StatusOK, rec.Code)

	var response operations.Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))

	data, ok := response.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "https://forky.testnet.example.com/snapshot/0d2855c9", data["url"])
}

func TestForkyLinkNodeKeepsPathSeparators(t *testing.T) {
	t.Parallel()

	svc := newForkyOperationService(http.DefaultClient, "https://forky.testnet.example.com")
	rec := httptest.NewRecorder()

	handled := svc.handleForkyOperation("forky.link_node", rec, newForkyOpRequest(t, map[string]any{
		"network": "testnet",
		"node":    "ethpandaops/testnet/utility-node 001",
	}))
	require.True(t, handled)
	require.Equal(t, http.StatusOK, rec.Code)

	var response operations.Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))

	data, ok := response.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t,
		"https://forky.testnet.example.com/node/ethpandaops/testnet/utility-node%20001",
		data["url"])
}

func TestForkyUnknownNetworkListsAvailable(t *testing.T) {
	t.Parallel()

	svc := newForkyOperationService(http.DefaultClient, "https://forky.testnet.example.com")
	rec := httptest.NewRecorder()

	handled := svc.handleForkyOperation("forky.get_now", rec, newForkyOpRequest(t, map[string]any{
		"network": "nope",
	}))
	require.True(t, handled)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "testnet")
}

func newForkyOperationService(httpClient *http.Client, forkyURL string) *service {
	log := logrus.New()
	log.SetOutput(io.Discard)

	return &service{
		log:        log,
		httpClient: httpClient,
		cartographoorClient: forkyOperationCartographoor{
			networks: map[string]discovery.Network{
				"testnet": {
					Name:   "testnet",
					Status: "active",
					ServiceURLs: &discovery.ServiceURLs{
						Forky: forkyURL,
					},
				},
			},
		},
	}
}

func newForkyOpRequest(t *testing.T, args map[string]any) *http.Request {
	t.Helper()

	body, err := json.Marshal(operations.Request{Args: args})
	require.NoError(t, err)

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/runtime/operations/forky",
		io.NopCloser(bytes.NewReader(body)),
	)
	req.Header.Set("Content-Type", "application/json")

	return req
}

type forkyOperationCartographoor struct {
	networks map[string]discovery.Network
}

func (c forkyOperationCartographoor) Start(_ context.Context) error { return nil }
func (c forkyOperationCartographoor) Stop() error                   { return nil }
func (c forkyOperationCartographoor) GetAllNetworks() map[string]discovery.Network {
	return c.networks
}
func (c forkyOperationCartographoor) GetActiveNetworks() map[string]discovery.Network {
	return c.networks
}
func (c forkyOperationCartographoor) GetNetwork(name string) (discovery.Network, bool) {
	network, ok := c.networks[name]
	return network, ok
}
func (c forkyOperationCartographoor) GetGroup(_ string) (map[string]discovery.Network, bool) {
	return nil, false
}
func (c forkyOperationCartographoor) GetGroups() []string { return nil }
func (c forkyOperationCartographoor) GetActiveGroups() map[string][]string {
	return nil
}
func (c forkyOperationCartographoor) IsDevnet(_ discovery.Network) bool {
	return false
}
func (c forkyOperationCartographoor) GetClusters(_ discovery.Network) []string {
	return nil
}
