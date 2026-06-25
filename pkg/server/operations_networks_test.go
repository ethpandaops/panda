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

func newNetworkOperationService() *service {
	log := logrus.New()
	log.SetOutput(io.Discard)

	return &service{
		log: log,
		cartographoorClient: networkOperationCartographoor{
			networks: map[string]discovery.Network{
				"mainnet": {
					Name:    "Mainnet",
					Status:  "active",
					ChainID: 1,
				},
				"fusaka-devnet-3": {
					Name:        "fusaka-devnet-3",
					Status:      "active",
					ChainID:     7088110746,
					Repository:  "ethpandaops/fusaka-devnets",
					Description: "Fusaka devnet 3",
					GenesisConfig: &discovery.GenesisConfig{
						GenesisTime:  1700000000,
						GenesisDelay: 60,
						API: []discovery.ConfigFile{
							{Path: "/api/v1/nodes/inventory", URL: "https://config.fusaka.example.com/api/v1/nodes/inventory"},
						},
					},
					ServiceURLs: &discovery.ServiceURLs{
						JSONRPC:   "https://rpc.fusaka.example.com",
						BeaconRPC: "https://beacon.fusaka.example.com",
						Dora:      "https://dora.fusaka.example.com",
					},
					Forks: &discovery.ForksConfig{
						Consensus: map[string]discovery.ConsensusForkConfig{
							"deneb":   {Epoch: 0, Timestamp: 1700000000},
							"electra": {Epoch: 10, Timestamp: 1700001000},
						},
						Execution: map[string]discovery.ExecutionForkConfig{
							"prague": {Block: 0, Timestamp: 1700000000},
						},
					},
					Images: &discovery.Images{
						Clients: []discovery.ClientImage{
							{Name: "geth", Version: "v1.15.0"},
							{Name: "lighthouse", Version: "v6.0.0"},
						},
						Tools: []discovery.ToolImage{
							{Name: "spamoor", Version: "latest"},
						},
					},
				},
				"old-devnet": {
					Name:       "old-devnet",
					Status:     "inactive",
					Repository: "ethpandaops/fusaka-devnets",
				},
			},
			groups: map[string][]string{
				"fusaka": {"fusaka-devnet-3", "old-devnet"},
			},
			activeGroups: map[string][]string{
				"fusaka": {"fusaka-devnet-3"},
			},
		},
	}
}

func newNetworkOpRequest(t *testing.T, args map[string]any) *http.Request {
	t.Helper()

	body, err := json.Marshal(operations.Request{Args: args})
	require.NoError(t, err)

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/runtime/operations/network",
		io.NopCloser(bytes.NewReader(body)),
	)
	req.Header.Set("Content-Type", "application/json")

	return req
}

func decodeNetworkData(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	var response operations.Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	data, ok := response.Data.(map[string]any)
	require.True(t, ok)

	return data
}

func TestNetworkListActiveOnly(t *testing.T) {
	t.Parallel()

	svc := newNetworkOperationService()
	rec := httptest.NewRecorder()

	handled := svc.handleNetworkOperation("network.list", rec, newNetworkOpRequest(t, map[string]any{}))

	require.True(t, handled)
	require.Equal(t, http.StatusOK, rec.Code)

	data := decodeNetworkData(t, rec)
	networks, ok := data["networks"].([]any)
	require.True(t, ok)
	require.Len(t, networks, 2) // mainnet + fusaka-devnet-3; old-devnet is inactive

	first, ok := networks[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "fusaka-devnet-3", first["id"]) // sorted by id
	assert.Equal(t, true, first["is_devnet"])
	assert.Equal(t, "fusaka", first["devnet_group"])

	groups, ok := data["groups"].([]any)
	require.True(t, ok)
	assert.Equal(t, []any{"fusaka"}, groups)
}

func TestNetworkListDevnetsOnly(t *testing.T) {
	t.Parallel()

	svc := newNetworkOperationService()
	rec := httptest.NewRecorder()

	handled := svc.handleNetworkOperation(
		"network.list", rec,
		newNetworkOpRequest(t, map[string]any{"devnets_only": true}),
	)

	require.True(t, handled)
	require.Equal(t, http.StatusOK, rec.Code)

	data := decodeNetworkData(t, rec)
	networks, ok := data["networks"].([]any)
	require.True(t, ok)
	require.Len(t, networks, 1)

	only, ok := networks[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "fusaka-devnet-3", only["id"])
}

func TestNetworkListIncludesInactiveWhenActiveFalse(t *testing.T) {
	t.Parallel()

	svc := newNetworkOperationService()
	rec := httptest.NewRecorder()

	handled := svc.handleNetworkOperation(
		"network.list", rec,
		newNetworkOpRequest(t, map[string]any{"active": false, "devnets_only": true}),
	)

	require.True(t, handled)
	require.Equal(t, http.StatusOK, rec.Code)

	data := decodeNetworkData(t, rec)
	networks, ok := data["networks"].([]any)
	require.True(t, ok)
	require.Len(t, networks, 2) // fusaka-devnet-3 + old-devnet
}

func TestNetworkGetCuratesDetail(t *testing.T) {
	t.Parallel()

	svc := newNetworkOperationService()
	rec := httptest.NewRecorder()

	handled := svc.handleNetworkOperation(
		"network.get", rec,
		newNetworkOpRequest(t, map[string]any{"network": "fusaka-devnet-3"}),
	)

	require.True(t, handled)
	require.Equal(t, http.StatusOK, rec.Code)

	data := decodeNetworkData(t, rec)
	assert.Equal(t, "fusaka-devnet-3", data["id"])
	assert.Equal(t, true, data["is_devnet"])
	assert.Equal(t, "fusaka", data["devnet_group"])
	assert.EqualValues(t, 7088110746, data["chain_id"])
	assert.EqualValues(t, 1700000000, data["genesis_time"])

	endpoints, ok := data["endpoints"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "https://rpc.fusaka.example.com", endpoints["rpc"])
	assert.Equal(t, "https://beacon.fusaka.example.com", endpoints["beacon"])
	assert.Equal(t, "https://dora.fusaka.example.com", endpoints["dora"])

	forks, ok := data["forks"].(map[string]any)
	require.True(t, ok)
	consensus, ok := forks["consensus"].(map[string]any)
	require.True(t, ok)
	electra, ok := consensus["electra"].(map[string]any)
	require.True(t, ok)
	assert.EqualValues(t, 10, electra["epoch"])

	clients, ok := data["clients"].([]any)
	require.True(t, ok)
	require.Len(t, clients, 2)

	assert.Equal(t,
		"https://config.fusaka.example.com/api/v1/nodes/inventory",
		data["node_inventory_url"],
	)
}

func TestNetworkGetNotFound(t *testing.T) {
	t.Parallel()

	svc := newNetworkOperationService()
	rec := httptest.NewRecorder()

	handled := svc.handleNetworkOperation(
		"network.get", rec,
		newNetworkOpRequest(t, map[string]any{"network": "nope"}),
	)

	require.True(t, handled)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestNetworkGetRequiresNetworkArg(t *testing.T) {
	t.Parallel()

	svc := newNetworkOperationService()
	rec := httptest.NewRecorder()

	handled := svc.handleNetworkOperation("network.get", rec, newNetworkOpRequest(t, map[string]any{}))

	require.True(t, handled)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestNetworkGroupListsMembers(t *testing.T) {
	t.Parallel()

	svc := newNetworkOperationService()
	rec := httptest.NewRecorder()

	handled := svc.handleNetworkOperation(
		"network.group", rec,
		newNetworkOpRequest(t, map[string]any{"group": "fusaka"}),
	)

	require.True(t, handled)
	require.Equal(t, http.StatusOK, rec.Code)

	data := decodeNetworkData(t, rec)
	assert.Equal(t, "fusaka", data["group"])
	networks, ok := data["networks"].([]any)
	require.True(t, ok)
	require.Len(t, networks, 2)
}

func TestNetworkGroupNotFound(t *testing.T) {
	t.Parallel()

	svc := newNetworkOperationService()
	rec := httptest.NewRecorder()

	handled := svc.handleNetworkOperation(
		"network.group", rec,
		newNetworkOpRequest(t, map[string]any{"group": "nope"}),
	)

	require.True(t, handled)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestNetworkOperationUnknownReturnsFalse(t *testing.T) {
	t.Parallel()

	svc := newNetworkOperationService()
	rec := httptest.NewRecorder()

	handled := svc.handleNetworkOperation("network.unknown", rec, newNetworkOpRequest(t, map[string]any{}))

	assert.False(t, handled)
}

type networkOperationCartographoor struct {
	networks     map[string]discovery.Network
	groups       map[string][]string
	activeGroups map[string][]string
}

func (c networkOperationCartographoor) Start(_ context.Context) error { return nil }
func (c networkOperationCartographoor) Stop() error                   { return nil }

func (c networkOperationCartographoor) GetAllNetworks() map[string]discovery.Network {
	return c.networks
}

func (c networkOperationCartographoor) GetActiveNetworks() map[string]discovery.Network {
	result := make(map[string]discovery.Network, len(c.networks))

	for id, network := range c.networks {
		if network.Status == "active" {
			result[id] = network
		}
	}

	return result
}

func (c networkOperationCartographoor) GetNetwork(name string) (discovery.Network, bool) {
	network, ok := c.networks[name]

	return network, ok
}

func (c networkOperationCartographoor) GetGroup(name string) (map[string]discovery.Network, bool) {
	ids, ok := c.groups[name]
	if !ok {
		return nil, false
	}

	result := make(map[string]discovery.Network, len(ids))

	for _, id := range ids {
		if network, found := c.networks[id]; found {
			result[id] = network
		}
	}

	return result, true
}

func (c networkOperationCartographoor) GetGroups() []string {
	names := make([]string, 0, len(c.groups))

	for name := range c.groups {
		names = append(names, name)
	}

	return names
}

func (c networkOperationCartographoor) GetActiveGroups() map[string][]string {
	return c.activeGroups
}
