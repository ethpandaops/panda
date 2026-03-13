package resource

import (
	"context"
	"strings"
	"testing"

	"github.com/ethpandaops/cartographoor/pkg/discovery"
	"github.com/sirupsen/logrus"
)

func TestActiveAndAllNetworkHandlersIncludeClustersAndGroups(t *testing.T) {
	t.Parallel()

	client := &testCartographoorClient{
		all: map[string]discovery.Network{
			"mainnet": {Name: "mainnet", ChainID: 1, Status: "active"},
			"holesky": {Name: "holesky", ChainID: 17000, Status: "inactive"},
		},
		active: map[string]discovery.Network{
			"mainnet": {Name: "mainnet", ChainID: 1, Status: "active"},
		},
		groups: map[string]map[string]discovery.Network{
			"devnets": {
				"holesky": {Name: "holesky", ChainID: 17000, Status: "inactive"},
			},
		},
		clusters: map[string][]string{
			"mainnet": {"xatu", "xatu-cbt"},
			"holesky": {"xatu-experimental"},
		},
	}

	activeContent, err := createActiveNetworksHandler(client)(context.Background(), "networks://active")
	if err != nil {
		t.Fatalf("createActiveNetworksHandler() error = %v", err)
	}

	var active NetworksActiveResponse
	decodeJSON(t, activeContent, &active)

	if len(active.Networks) != 1 || active.Networks[0].Name != "mainnet" {
		t.Fatalf("active networks = %#v, want mainnet summary", active.Networks)
	}

	if got := strings.Join(active.Networks[0].Clusters, ","); got != "xatu,xatu-cbt" {
		t.Fatalf("active clusters = %q, want xatu,xatu-cbt", got)
	}

	allContent, err := createAllNetworksHandler(client)(context.Background(), "networks://all")
	if err != nil {
		t.Fatalf("createAllNetworksHandler() error = %v", err)
	}

	var all NetworksAllResponse
	decodeJSON(t, allContent, &all)

	if got := strings.Join(all.Networks["holesky"].Clusters, ","); got != "xatu-experimental" {
		t.Fatalf("all clusters = %q, want xatu-experimental", got)
	}
}

func TestNetworkDetailHandlerHandlesNetworkGroupAndNotFound(t *testing.T) {
	t.Parallel()

	client := &testCartographoorClient{
		all: map[string]discovery.Network{
			"mainnet": {Name: "mainnet", ChainID: 1, Status: "active"},
		},
		groups: map[string]map[string]discovery.Network{
			"devnets": {
				"devnet-1": {Name: "devnet-1", ChainID: 701, Status: "active"},
			},
		},
		clusters: map[string][]string{
			"mainnet":  {"xatu"},
			"devnet-1": {"xatu-cbt"},
		},
	}

	handler := createNetworkDetailHandler(logrus.New(), client)

	networkContent, err := handler(context.Background(), "networks://mainnet")
	if err != nil {
		t.Fatalf("network detail error = %v", err)
	}

	var network NetworkDetailResponse
	decodeJSON(t, networkContent, &network)

	if network.Network.Name != "mainnet" || len(network.Network.Clusters) != 1 {
		t.Fatalf("network detail = %#v, want mainnet with clusters", network.Network)
	}

	groupContent, err := handler(context.Background(), "networks://devnets")
	if err != nil {
		t.Fatalf("group detail error = %v", err)
	}

	var group GroupDetailResponse
	decodeJSON(t, groupContent, &group)

	if group.Group != "devnets" || group.Networks["devnet-1"].ChainID != 701 {
		t.Fatalf("group detail = %#v, want devnets response", group)
	}

	if _, err := handler(context.Background(), "networks://missing"); err == nil || !strings.Contains(err.Error(), `Available groups: devnets`) {
		t.Fatalf("missing network error = %v, want available groups hint", err)
	}
}

func TestRegisterNetworksResourcesAddsStaticAndTemplateResources(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(logrus.New())
	RegisterNetworksResources(logrus.New(), reg, &testCartographoorClient{})

	if got := len(reg.ListStatic()); got != 2 {
		t.Fatalf("ListStatic() len = %d, want 2", got)
	}

	templates := reg.ListTemplates()
	if len(templates) != 1 {
		t.Fatalf("ListTemplates() len = %d, want 1", len(templates))
	}

	if raw := templates[0].URITemplate.Raw(); raw != "networks://{name}" {
		t.Fatalf("template URI = %q, want networks://{name}", raw)
	}
}
