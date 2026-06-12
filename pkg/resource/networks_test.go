package resource

import (
	"context"
	"encoding/json"
	"io"
	"sort"
	"testing"

	"github.com/ethpandaops/cartographoor/pkg/discovery"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/surface"
)

type fakeNetworkClient struct {
	networks map[string]discovery.Network
	groups   map[string]map[string]discovery.Network
}

func (f *fakeNetworkClient) Start(_ context.Context) error { return nil }
func (f *fakeNetworkClient) Stop() error                   { return nil }
func (f *fakeNetworkClient) GetAllNetworks() map[string]discovery.Network {
	return f.networks
}
func (f *fakeNetworkClient) GetActiveNetworks() map[string]discovery.Network {
	out := make(map[string]discovery.Network)
	for id, network := range f.networks {
		if network.Status == "active" {
			out[id] = network
		}
	}

	return out
}
func (f *fakeNetworkClient) GetNetwork(name string) (discovery.Network, bool) {
	network, ok := f.networks[name]

	return network, ok
}
func (f *fakeNetworkClient) GetGroup(name string) (map[string]discovery.Network, bool) {
	group, ok := f.groups[name]

	return group, ok
}
func (f *fakeNetworkClient) GetGroups() []string {
	groups := make([]string, 0, len(f.groups))
	for group := range f.groups {
		groups = append(groups, group)
	}

	sort.Strings(groups)

	return groups
}
func (f *fakeNetworkClient) GetActiveGroups() map[string][]string {
	out := make(map[string][]string, len(f.groups))

	for group, networks := range f.groups {
		names := make([]string, 0, len(networks))
		for id, network := range networks {
			if network.Status == "active" {
				names = append(names, id)
			}
		}

		if len(names) == 0 {
			continue
		}

		sort.Strings(names)
		out[group] = names
	}

	return out
}

func TestActiveNetworksIncludesStableIDs(t *testing.T) {
	t.Parallel()

	client := &fakeNetworkClient{
		networks: map[string]discovery.Network{
			"group-a-devnet-1": {Name: "devnet-1", ChainID: 11, Status: "active"},
			"group-b-devnet-1": {Name: "devnet-1", ChainID: 12, Status: "active"},
		},
		groups: map[string]map[string]discovery.Network{
			"group-a": {
				"group-a-devnet-1": {Name: "devnet-1", ChainID: 11, Status: "active"},
			},
			"group-b": {
				"group-b-devnet-1": {Name: "devnet-1", ChainID: 12, Status: "active"},
			},
		},
	}

	out, err := createActiveNetworksHandler(client)(context.Background(), "networks://active", surface.MCP)
	require.NoError(t, err)

	var response NetworksActiveResponse
	require.NoError(t, json.Unmarshal([]byte(out), &response))
	require.Len(t, response.Networks, 2)
	require.Equal(t, "group-a-devnet-1", response.Networks[0].ID)
	require.Equal(t, "networks://group-a-devnet-1", response.Networks[0].ResourceURI)
	require.True(t, response.Networks[0].IsDevnet)
	require.Equal(t, "group-a", response.Networks[0].DevnetGroup)
	require.Equal(t, []string{"group-a", "group-b"}, response.Groups)
	require.Equal(t, map[string][]string{
		"group-a": {"group-a-devnet-1"},
		"group-b": {"group-b-devnet-1"},
	}, response.ActiveDevnetGroups)
	require.Contains(t, response.Usage, "display label")
	require.Contains(t, response.Usage, "authoritative live Cartographoor inventory")
}

func TestActiveNetworksOnlyListsActiveDevnetGroups(t *testing.T) {
	t.Parallel()

	client := &fakeNetworkClient{
		networks: map[string]discovery.Network{
			"mainnet":            {Name: "mainnet", ChainID: 1, Status: "active"},
			"group-a-devnet-1":   {Name: "devnet-1", ChainID: 11, Status: "active"},
			"group-a-devnet-old": {Name: "devnet-old", ChainID: 10, Status: "inactive"},
			"group-b-devnet-old": {Name: "devnet-old", ChainID: 20, Status: "inactive"},
		},
		groups: map[string]map[string]discovery.Network{
			"group-a": {
				"group-a-devnet-1":   {Name: "devnet-1", ChainID: 11, Status: "active"},
				"group-a-devnet-old": {Name: "devnet-old", ChainID: 10, Status: "inactive"},
			},
			"group-b": {
				"group-b-devnet-old": {Name: "devnet-old", ChainID: 20, Status: "inactive"},
			},
		},
	}

	out, err := createActiveNetworksHandler(client)(context.Background(), "networks://active", surface.MCP)
	require.NoError(t, err)

	var response NetworksActiveResponse
	require.NoError(t, json.Unmarshal([]byte(out), &response))
	require.Equal(t, []string{"group-a"}, response.Groups)
	require.Equal(t, map[string][]string{"group-a": {"group-a-devnet-1"}}, response.ActiveDevnetGroups)

	byID := make(map[string]NetworkSummary, len(response.Networks))
	for _, network := range response.Networks {
		byID[network.ID] = network
	}

	require.False(t, byID["mainnet"].IsDevnet)
	require.True(t, byID["group-a-devnet-1"].IsDevnet)
	require.NotContains(t, byID, "group-a-devnet-old")
}

func TestNetworkDetailErrorSuggestsIDForDisplayName(t *testing.T) {
	t.Parallel()

	client := &fakeNetworkClient{
		networks: map[string]discovery.Network{
			"group-a-devnet-1": {Name: "devnet-1", ChainID: 11, Status: "active"},
		},
		groups: map[string]map[string]discovery.Network{},
	}

	log := logrus.New()
	log.SetOutput(io.Discard)

	_, err := createNetworkDetailHandler(log, client)(context.Background(), "networks://devnet-1", surface.MCP)
	require.Error(t, err)
	require.Contains(t, err.Error(), "use full network id: group-a-devnet-1")
	require.Contains(t, err.Error(), "networks://active")
}

func TestNetworkDetailIncludesID(t *testing.T) {
	t.Parallel()

	client := &fakeNetworkClient{
		networks: map[string]discovery.Network{
			"group-a-devnet-1": {
				Name:    "devnet-1",
				ChainID: 11,
				Status:  "active",
				GenesisConfig: &discovery.GenesisConfig{
					API: []discovery.ConfigFile{{
						Path: "/api/v1/nodes/inventory",
						URL:  "https://config.example/api/v1/nodes/inventory",
					}},
				},
			},
		},
	}

	log := logrus.New()
	log.SetOutput(io.Discard)

	out, err := createNetworkDetailHandler(log, client)(context.Background(), "networks://group-a-devnet-1", surface.MCP)
	require.NoError(t, err)

	var response NetworkDetailResponse
	require.NoError(t, json.Unmarshal([]byte(out), &response))
	require.Equal(t, "group-a-devnet-1", response.ID)
	require.Equal(t, "networks://group-a-devnet-1", response.ResourceURI)
	require.Equal(t, "https://config.example/api/v1/nodes/inventory", response.NodeInventoryURL)
	require.Contains(t, response.Usage, "node_inventory_url")
}
