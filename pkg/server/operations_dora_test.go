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

func TestDoraNetworkOverviewUsesNativeOverviewEndpoint(t *testing.T) {
	t.Parallel()

	doraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/network/overview":
			_, _ = w.Write([]byte(`{"status":"OK","data":{
				"is_synced":true,
				"current_slot":3210,
				"current_epoch":200,
				"finalized_epoch":198,
				"epochs_since_finality":2,
				"finalizing":true,
				"active_validator_count":10,
				"total_validator_count":12,
				"pending_validator_count":1,
				"exited_validator_count":1,
				"data_quality_warnings":["vote aggregate likely incomplete"],
				"participation":{"rate":null,"source":"attestation_index","complete":false},
				"current_state":{"current_slot":3211,"current_epoch":200,"slots_per_epoch":16},
				"metadata":{"slots_per_epoch":16}
			}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(doraServer.Close)

	svc := newDoraOperationService(doraServer.Client(), doraServer.URL)
	rec := httptest.NewRecorder()

	handled := svc.handleDoraOperation("dora.get_network_overview", rec, newDoraOpRequest(t, "testnet"))
	require.True(t, handled)
	require.Equal(t, http.StatusOK, rec.Code)

	var response operations.Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))

	data, ok := response.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(200), data["current_epoch"])
	assert.Equal(t, float64(3210), data["current_slot"])
	assert.Equal(t, float64(198), data["finalized_epoch"])
	// Start slots derive from the reported slots_per_epoch, not the mainnet default.
	assert.Equal(t, float64(3200), data["current_epoch_start_slot"])
	assert.Equal(t, float64(3168), data["finalized_epoch_start_slot"])
	assert.Equal(t, float64(2), data["epochs_since_finality"])
	assert.Equal(t, true, data["finalizing"])
	assert.NotContains(t, data, "finalized")
	assert.Equal(t, true, data["is_synced"])
	assert.Equal(t, float64(10), data["active_validator_count"])
	assert.NotEmpty(t, data["data_quality_warnings"])

	participation, ok := data["participation"].(map[string]any)
	require.True(t, ok)
	assert.Nil(t, participation["rate"])
	assert.Equal(t, false, participation["complete"])
}

func TestDoraNetworkOverviewRejectsLegacyOverviewShape(t *testing.T) {
	t.Parallel()

	doraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/network/overview":
			// Older Dora releases expose the endpoint without the flattened
			// top-level summary fields.
			_, _ = w.Write([]byte(`{"status":"OK","data":{"network_info":{"network_name":"testnet"},"current_state":{"current_slot":3210,"current_epoch":100,"slots_per_epoch":32},"checkpoints":{"finalized_epoch":98},"is_synced":true}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(doraServer.Close)

	svc := newDoraOperationService(doraServer.Client(), doraServer.URL)
	rec := httptest.NewRecorder()

	handled := svc.handleDoraOperation("dora.get_network_overview", rec, newDoraOpRequest(t, "testnet"))
	require.True(t, handled)
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Contains(t, rec.Body.String(), "upgrade Dora")
}

func TestDoraNetworkOverviewRejectsDoraErrorEnvelope(t *testing.T) {
	t.Parallel()

	doraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/network/overview":
			_, _ = w.Write([]byte(`{"status":"ERROR: upstream index unavailable","data":null}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(doraServer.Close)

	svc := newDoraOperationService(doraServer.Client(), doraServer.URL)
	rec := httptest.NewRecorder()

	handled := svc.handleDoraOperation("dora.get_network_overview", rec, newDoraOpRequest(t, "testnet"))
	require.True(t, handled)
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Contains(t, rec.Body.String(), "dora API error")
}

func TestDoraDataPassthroughEscapesIdentifier(t *testing.T) {
	t.Parallel()

	doraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/slot/abc%2Fdef%3Fx=1", r.URL.EscapedPath())
		assert.Empty(t, r.URL.RawQuery)
		_, _ = w.Write([]byte(`{"status":"OK","data":{"slot":"escaped"}}`))
	}))
	t.Cleanup(doraServer.Close)

	svc := newDoraOperationService(doraServer.Client(), doraServer.URL)
	rec := httptest.NewRecorder()

	handled := svc.handleDoraOperation("dora.get_slot", rec, newDoraOpRequestWithArgs(t, map[string]any{
		"network":      "testnet",
		"slot_or_hash": "abc/def?x=1",
	}))
	require.True(t, handled)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"slot":"escaped"`)
}

func newDoraOperationService(httpClient *http.Client, doraURL string) *service {
	log := logrus.New()
	log.SetOutput(io.Discard)

	return &service{
		log:        log,
		httpClient: httpClient,
		cartographoorClient: doraOperationCartographoor{
			networks: map[string]discovery.Network{
				"testnet": {
					Name:   "testnet",
					Status: "active",
					ServiceURLs: &discovery.ServiceURLs{
						Dora: doraURL,
					},
				},
			},
		},
	}
}

func newDoraOpRequest(t *testing.T, network string) *http.Request {
	t.Helper()

	return newDoraOpRequestWithArgs(t, map[string]any{"network": network})
}

func newDoraOpRequestWithArgs(t *testing.T, args map[string]any) *http.Request {
	t.Helper()

	body, err := json.Marshal(operations.Request{Args: args})
	require.NoError(t, err)

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/runtime/operations/dora",
		io.NopCloser(bytes.NewReader(body)),
	)
	req.Header.Set("Content-Type", "application/json")

	return req
}

type doraOperationCartographoor struct {
	networks map[string]discovery.Network
}

func (c doraOperationCartographoor) Start(_ context.Context) error { return nil }
func (c doraOperationCartographoor) Stop() error                   { return nil }
func (c doraOperationCartographoor) GetAllNetworks() map[string]discovery.Network {
	return c.networks
}
func (c doraOperationCartographoor) GetActiveNetworks() map[string]discovery.Network {
	return c.networks
}
func (c doraOperationCartographoor) GetNetwork(name string) (discovery.Network, bool) {
	network, ok := c.networks[name]
	return network, ok
}
func (c doraOperationCartographoor) GetGroup(_ string) (map[string]discovery.Network, bool) {
	return nil, false
}
func (c doraOperationCartographoor) GetGroups() []string { return nil }
func (c doraOperationCartographoor) GetActiveGroups() map[string][]string {
	return nil
}
func (c doraOperationCartographoor) IsDevnet(_ discovery.Network) bool {
	return false
}
func (c doraOperationCartographoor) GetClusters(_ discovery.Network) []string {
	return nil
}
