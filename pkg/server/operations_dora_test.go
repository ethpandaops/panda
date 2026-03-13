package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethpandaops/cartographoor/pkg/discovery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/operations"
)

func TestDoraListNetworksFiltersAndSorts(t *testing.T) {
	t.Parallel()

	srv := newOperationTestService(
		&stubProxyService{url: "http://proxy.test"},
		&stubCartographoorClient{
			activeNetworks: map[string]discovery.Network{
				"zeta":  {ServiceURLs: &discovery.ServiceURLs{Dora: "https://zeta.example"}},
				"alpha": {ServiceURLs: &discovery.ServiceURLs{Dora: "https://alpha.example"}},
				"beta":  {ServiceURLs: &discovery.ServiceURLs{}},
				"gamma": {},
			},
		},
		http.DefaultClient,
	)

	rec := performOperationRequest(t, srv, "dora.list_networks", operations.Request{})
	require.Equal(t, http.StatusOK, rec.Code)

	var response operations.Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))

	payload, err := operations.DecodeResponseData[operations.DoraNetworksPayload](&response)
	require.NoError(t, err)
	assert.Equal(
		t,
		[]operations.DoraNetwork{
			{Name: "alpha", DoraURL: "https://alpha.example"},
			{Name: "zeta", DoraURL: "https://zeta.example"},
		},
		payload.Networks,
	)
}

func TestDoraBaseURLValidationAndAvailability(t *testing.T) {
	t.Parallel()

	t.Run("requires network", func(t *testing.T) {
		t.Parallel()

		srv := newOperationTestService(
			&stubProxyService{url: "http://proxy.test"},
			&stubCartographoorClient{
				activeNetworks: map[string]discovery.Network{
					"hoodi": {ServiceURLs: &discovery.ServiceURLs{Dora: "https://dora.example"}},
				},
			},
			http.DefaultClient,
		)

		rec := performOperationRequest(t, srv, "dora.get_base_url", operations.Request{})
		require.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "network is required")
	})

	t.Run("reports dora unavailable", func(t *testing.T) {
		t.Parallel()

		srv := newOperationTestService(
			&stubProxyService{url: "http://proxy.test"},
			nil,
			http.DefaultClient,
		)

		rec := performOperationRequest(
			t,
			srv,
			"dora.get_base_url",
			operations.TypedRequest[operations.DoraNetworkArgs]{Args: operations.DoraNetworkArgs{Network: "hoodi"}},
		)
		require.Equal(t, http.StatusServiceUnavailable, rec.Code)
		assert.Contains(t, rec.Body.String(), "dora is unavailable")
	})
}

func TestDoraIdentifierValidation(t *testing.T) {
	t.Parallel()

	srv := newOperationTestService(
		&stubProxyService{url: "http://proxy.test"},
		&stubCartographoorClient{
			activeNetworks: map[string]discovery.Network{
				"hoodi": {ServiceURLs: &discovery.ServiceURLs{Dora: "https://dora.example"}},
			},
		},
		http.DefaultClient,
	)

	rec := performOperationRequest(
		t,
		srv,
		"dora.get_validator",
		operations.TypedRequest[operations.DoraIndexOrPubkeyArgs]{Args: operations.DoraIndexOrPubkeyArgs{
			Network: "hoodi",
		}},
	)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "index_or_pubkey is required")
}

func TestDoraNetworkOverviewShapesMissingValidatorInfo(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/epoch/head", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"epoch":"7","finalized":false,"globalparticipationrate":0.75}}`))
	}))
	defer upstream.Close()

	srv := newOperationTestService(
		&stubProxyService{url: "http://proxy.test"},
		&stubCartographoorClient{
			activeNetworks: map[string]discovery.Network{
				"hoodi": {ServiceURLs: &discovery.ServiceURLs{Dora: upstream.URL}},
			},
		},
		upstream.Client(),
	)

	rec := performOperationRequest(
		t,
		srv,
		"dora.get_network_overview",
		operations.TypedRequest[operations.DoraNetworkArgs]{Args: operations.DoraNetworkArgs{Network: "hoodi"}},
	)
	require.Equal(t, http.StatusOK, rec.Code)

	var response operations.Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))

	payload, err := operations.DecodeResponseData[operations.DoraOverviewPayload](&response)
	require.NoError(t, err)
	assert.Equal(t, "hoodi", response.Meta["network"])
	assert.Equal(t, "7", payload.CurrentEpoch)
	assert.EqualValues(t, 224, payload.CurrentSlot)
	assert.Equal(t, false, payload.Finalized)
	assert.EqualValues(t, 0.75, payload.ParticipationRate)
	assert.Nil(t, payload.ActiveValidatorCount)
	assert.Nil(t, payload.TotalValidatorCount)
	assert.Nil(t, payload.PendingValidatorCount)
	assert.Nil(t, payload.ExitedValidatorCount)
}

func TestDoraValidatorsPassthroughDefaultsContentType(t *testing.T) {
	t.Parallel()

	client := &http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			assert.Equal(t, "/api/v1/validators", r.URL.Path)
			assert.Equal(t, "100", r.URL.Query().Get("limit"))
			assert.Empty(t, r.URL.Query().Get("status"))

			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"data":["validator-1"]}`)),
			}, nil
		}),
	}

	srv := newOperationTestService(
		&stubProxyService{url: "http://proxy.test"},
		&stubCartographoorClient{
			activeNetworks: map[string]discovery.Network{
				"hoodi": {ServiceURLs: &discovery.ServiceURLs{Dora: "https://dora.example"}},
			},
		},
		client,
	)

	rec := performOperationRequest(
		t,
		srv,
		"dora.get_validators",
		operations.Request{Args: map[string]any{"network": "hoodi"}},
	)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "passthrough", rec.Header().Get("X-Operation-Transport"))
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.JSONEq(t, `{"data":["validator-1"]}`, rec.Body.String())
}

func TestDoraGetSlotPropagatesUpstreamFailureContext(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/slot/123", r.URL.Path)
		http.Error(w, "slot fetch failed", http.StatusBadGateway)
	}))
	defer upstream.Close()

	srv := newOperationTestService(
		&stubProxyService{url: "http://proxy.test"},
		&stubCartographoorClient{
			activeNetworks: map[string]discovery.Network{
				"hoodi": {ServiceURLs: &discovery.ServiceURLs{Dora: upstream.URL}},
			},
		},
		upstream.Client(),
	)

	rec := performOperationRequest(
		t,
		srv,
		"dora.get_slot",
		operations.TypedRequest[operations.DoraSlotOrHashArgs]{Args: operations.DoraSlotOrHashArgs{
			Network:    "hoodi",
			SlotOrHash: "123",
		}},
	)
	require.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Contains(t, rec.Body.String(), "dora.http upstream failure")
	assert.Contains(t, rec.Body.String(), "status 502")
	assert.Contains(t, rec.Body.String(), "/api/v1/slot/123")
	assert.Contains(t, rec.Body.String(), "slot fetch failed")
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
