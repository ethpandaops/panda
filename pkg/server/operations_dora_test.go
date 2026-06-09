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

func TestDoraNetworkOverviewDerivesFinalityFromFinalizedEpoch(t *testing.T) {
	t.Parallel()

	doraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/epoch/latest":
			_, _ = w.Write([]byte(`{"status":"OK","data":{"epoch":100,"finalized":false,"globalparticipationrate":64,"validatorinfo":{"active":10,"total":12,"pending":1,"exited":1}}}`))
		case "/api/v1/epoch/finalized":
			_, _ = w.Write([]byte(`{"status":"OK","data":{"epoch":98,"finalized":true}}`))
		case "/api/v1/slots":
			assert.Equal(t, "1", r.URL.Query().Get("limit"))
			_, _ = w.Write([]byte(`{"status":"OK","data":{"slots":[{"slot":3210,"epoch":100}]}}`))
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
	assert.Equal(t, float64(100), data["current_epoch"])
	assert.Equal(t, float64(98), data["finalized_epoch"])
	assert.Equal(t, float64(3210), data["current_slot"])
	assert.Equal(t, float64(3200), data["current_epoch_start_slot"])
	assert.Equal(t, float64(3136), data["finalized_epoch_start_slot"])
	assert.NotContains(t, data, "finalized_slot")
	assert.Equal(t, float64(2), data["epochs_since_finality"])
	assert.Equal(t, true, data["finalized"])
	assert.NotContains(t, data, "participation_rate")
	assert.NotEmpty(t, data["data_quality_warnings"])
	assert.Equal(t, float64(10), data["active_validator_count"])
}

func TestDoraNetworkOverviewRejectsDoraErrorEnvelope(t *testing.T) {
	t.Parallel()

	doraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/epoch/latest":
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
func (c doraOperationCartographoor) IsDevnet(_ discovery.Network) bool {
	return false
}
func (c doraOperationCartographoor) GetClusters(_ discovery.Network) []string {
	return nil
}
