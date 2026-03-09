package resource

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/ethpandaops/cartographoor/pkg/discovery"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/mcp/pkg/cartographoor"
)

// networkURIPattern matches networks://{name} URIs.
var networkURIPattern = regexp.MustCompile(`^networks://(.+)$`)

// NetworkSummary is a compact representation for the active networks list.
type NetworkSummary struct {
	Name     string   `json:"name"`
	ChainID  uint64   `json:"chain_id,omitempty"`
	Clusters []string `json:"clusters"`
	Status   string   `json:"status"`
}

// NetworksActiveResponse is the response for networks://active.
type NetworksActiveResponse struct {
	Networks []NetworkSummary `json:"networks"`
	Groups   []string         `json:"groups"`
	Usage    string           `json:"usage"`
}

// NetworkWithClusters wraps a discovery.Network with xatu-specific cluster info.
type NetworkWithClusters struct {
	discovery.Network
	Clusters []string `json:"clusters"`
}

// NetworksAllResponse is the response for networks://all.
type NetworksAllResponse struct {
	Networks map[string]NetworkWithClusters `json:"networks"`
	Groups   []string                       `json:"groups"`
}

// NetworkDetailResponse is the response for networks://{name} (single network).
type NetworkDetailResponse struct {
	Network NetworkWithClusters `json:"network"`
}

// GroupDetailResponse is the response for networks://{group} (devnet group).
type GroupDetailResponse struct {
	Group    string                         `json:"group"`
	Networks map[string]NetworkWithClusters `json:"networks"`
}

// RegisterNetworksResources registers all network-related resources with the registry.
func RegisterNetworksResources(log logrus.FieldLogger, reg Registry, client cartographoor.CartographoorClient) {
	log = log.WithField("resource", "networks")

	// Register networks://active - compact list of active networks
	reg.RegisterStatic(StaticResource{
		Resource: mcp.NewResource(
			"networks://active",
			"Active Networks",
			mcp.WithResourceDescription("Compact list of active Ethereum networks and available devnet groups"),
			mcp.WithMIMEType("application/json"),
			mcp.WithAnnotations([]mcp.Role{mcp.RoleAssistant}, 0.7),
		),
		Handler: createActiveNetworksHandler(client),
	})

	// Register networks://all - all networks including inactive
	reg.RegisterStatic(StaticResource{
		Resource: mcp.NewResource(
			"networks://all",
			"All Networks",
			mcp.WithResourceDescription("All Ethereum networks including inactive ones"),
			mcp.WithMIMEType("application/json"),
			mcp.WithAnnotations([]mcp.Role{mcp.RoleAssistant}, 0.4),
		),
		Handler: createAllNetworksHandler(client),
	})

	// Register networks://{name} - single network or devnet group
	reg.RegisterTemplate(TemplateResource{
		Template: mcp.NewResourceTemplate(
			"networks://{name}",
			"Network or Group Details",
			mcp.WithTemplateDescription("Get details for a specific network or all networks in a devnet group"),
			mcp.WithTemplateMIMEType("application/json"),
			mcp.WithTemplateAnnotations([]mcp.Role{mcp.RoleAssistant}, 0.5),
		),
		Pattern: networkURIPattern,
		Handler: createNetworkDetailHandler(log, client),
	})

	log.Debug("Registered networks resources")
}

// createActiveNetworksHandler returns a handler for networks://active.
func createActiveNetworksHandler(client cartographoor.CartographoorClient) ReadHandler {
	return func(_ context.Context, _ string) (string, error) {
		networks := client.GetActiveNetworks()
		groups := client.GetGroups()

		summaries := make([]NetworkSummary, 0, len(networks))

		for _, network := range networks {
			summaries = append(summaries, NetworkSummary{
				Name:     network.Name,
				ChainID:  network.ChainID,
				Clusters: client.GetClusters(network),
				Status:   network.Status,
			})
		}

		response := NetworksActiveResponse{
			Networks: summaries,
			Groups:   groups,
			Usage:    "Use networks://{name} for full network details or networks://{group} for all networks in a devnet group",
		}

		data, err := json.MarshalIndent(response, "", "  ")
		if err != nil {
			return "", fmt.Errorf("marshaling response: %w", err)
		}

		return string(data), nil
	}
}

// createAllNetworksHandler returns a handler for networks://all.
func createAllNetworksHandler(client cartographoor.CartographoorClient) ReadHandler {
	return func(_ context.Context, _ string) (string, error) {
		networks := client.GetAllNetworks()
		groups := client.GetGroups()

		networksWithClusters := make(map[string]NetworkWithClusters, len(networks))

		for name, network := range networks {
			networksWithClusters[name] = NetworkWithClusters{
				Network:  network,
				Clusters: client.GetClusters(network),
			}
		}

		response := NetworksAllResponse{
			Networks: networksWithClusters,
			Groups:   groups,
		}

		data, err := json.MarshalIndent(response, "", "  ")
		if err != nil {
			return "", fmt.Errorf("marshaling response: %w", err)
		}

		return string(data), nil
	}
}

// createNetworkDetailHandler returns a handler for networks://{name}.
func createNetworkDetailHandler(log logrus.FieldLogger, client cartographoor.CartographoorClient) ReadHandler {
	return func(_ context.Context, uri string) (string, error) {
		matches := networkURIPattern.FindStringSubmatch(uri)
		if len(matches) != 2 {
			return "", fmt.Errorf("invalid URI format: %s", uri)
		}

		name := matches[1]

		// Try exact network match first
		if network, ok := client.GetNetwork(name); ok {
			response := NetworkDetailResponse{
				Network: NetworkWithClusters{
					Network:  network,
					Clusters: client.GetClusters(network),
				},
			}

			data, err := json.MarshalIndent(response, "", "  ")
			if err != nil {
				return "", fmt.Errorf("marshaling response: %w", err)
			}

			return string(data), nil
		}

		// Try group match
		if networks, ok := client.GetGroup(name); ok {
			networksWithClusters := make(map[string]NetworkWithClusters, len(networks))

			for netName, network := range networks {
				networksWithClusters[netName] = NetworkWithClusters{
					Network:  network,
					Clusters: client.GetClusters(network),
				}
			}

			response := GroupDetailResponse{
				Group:    name,
				Networks: networksWithClusters,
			}

			data, err := json.MarshalIndent(response, "", "  ")
			if err != nil {
				return "", fmt.Errorf("marshaling response: %w", err)
			}

			return string(data), nil
		}

		// Not found - provide helpful error
		groups := client.GetGroups()
		allNetworks := client.GetAllNetworks()
		networkNames := make([]string, 0, len(allNetworks))

		for netName := range allNetworks {
			networkNames = append(networkNames, netName)
		}

		log.WithFields(logrus.Fields{
			"requested": name,
			"networks":  len(networkNames),
			"groups":    len(groups),
		}).Debug("Network or group not found")

		return "", fmt.Errorf(
			"network or group %q not found. Available groups: %s",
			name,
			strings.Join(groups, ", "),
		)
	}
}
