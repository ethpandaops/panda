package resource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/ethpandaops/cartographoor/pkg/discovery"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/cartographoor"
	"github.com/ethpandaops/panda/pkg/surface"
)

// networkURIPattern matches networks://{name} URIs.
var networkURIPattern = regexp.MustCompile(`^networks://(.+)$`)

// NetworkSummary is a compact representation for the active networks list.
type NetworkSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ChainID     uint64 `json:"chain_id,omitempty"`
	Status      string `json:"status"`
	IsDevnet    bool   `json:"is_devnet"`
	DevnetGroup string `json:"devnet_group,omitempty"`
	ResourceURI string `json:"resource_uri"`
}

// NetworksActiveResponse is the response for networks://active.
type NetworksActiveResponse struct {
	Networks           []NetworkSummary    `json:"networks"`
	Groups             []string            `json:"groups"`
	ActiveDevnetGroups map[string][]string `json:"active_devnet_groups"`
	Usage              string              `json:"usage"`
}

// NetworksAllResponse is the response for networks://all.
type NetworksAllResponse struct {
	Networks map[string]discovery.Network `json:"networks"`
	Groups   []string                     `json:"groups"`
}

// NetworkDetailResponse is the response for networks://{name} (single network).
type NetworkDetailResponse struct {
	ID               string            `json:"id"`
	ResourceURI      string            `json:"resource_uri"`
	NodeInventoryURL string            `json:"node_inventory_url,omitempty"`
	Usage            string            `json:"usage,omitempty"`
	Network          discovery.Network `json:"network"`
}

// GroupDetailResponse is the response for networks://{group} (devnet group).
type GroupDetailResponse struct {
	Group    string                       `json:"group"`
	Networks map[string]discovery.Network `json:"networks"`
}

// RegisterNetworksResources registers all network-related resources with the registry.
func RegisterNetworksResources(log logrus.FieldLogger, reg Registry, client cartographoor.CartographoorClient) {
	log = log.WithField("resource", "networks")

	// Register networks://active - compact list of active networks
	reg.RegisterStatic(StaticResource{
		Resource: mcp.NewResource(
			"networks://active",
			"Active Networks",
			mcp.WithResourceDescription("Compact Cartographoor list of active Ethereum networks and active devnet groups"),
			mcp.WithMIMEType("application/json"),
			mcp.WithAnnotations([]mcp.Role{mcp.RoleAssistant}, 0.7, ""),
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
			mcp.WithAnnotations([]mcp.Role{mcp.RoleAssistant}, 0.4, ""),
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
			mcp.WithTemplateAnnotations([]mcp.Role{mcp.RoleAssistant}, 0.5, ""),
		),
		Pattern: networkURIPattern,
		Handler: createNetworkDetailHandler(log, client),
	})

	log.Debug("Registered networks resources")
}

// createActiveNetworksHandler returns a handler for networks://active.
func createActiveNetworksHandler(client cartographoor.CartographoorClient) ReadHandler {
	return func(_ context.Context, _ string, _ surface.Dialect) (string, error) {
		networks := client.GetActiveNetworks()
		activeGroups := client.GetActiveGroups()
		groups := sortedGroupNames(activeGroups)
		networkGroups := networkGroupIndex(activeGroups)

		summaries := make([]NetworkSummary, 0, len(networks))

		for id, network := range networks {
			group := networkGroups[id]

			summaries = append(summaries, NetworkSummary{
				ID:          id,
				Name:        network.Name,
				ChainID:     network.ChainID,
				Status:      network.Status,
				IsDevnet:    group != "",
				DevnetGroup: group,
				ResourceURI: "networks://" + id,
			})
		}

		sort.Slice(summaries, func(i, j int) bool {
			return summaries[i].ID < summaries[j].ID
		})

		response := NetworksActiveResponse{
			Networks:           summaries,
			Groups:             groups,
			ActiveDevnetGroups: activeGroups,
			Usage:              "This is the authoritative live Cartographoor inventory for current network and devnet ids. For active devnets, use active_devnet_groups or filter networks where is_devnet is true. Use each network's resource_uri, or networks://{id}, for full network details. The name field is a display label and may be short or duplicated. Use networks://{group} for all networks in a devnet group.",
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
	return func(_ context.Context, _ string, _ surface.Dialect) (string, error) {
		response := NetworksAllResponse{
			Networks: client.GetAllNetworks(),
			Groups:   client.GetGroups(),
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
	return func(_ context.Context, uri string, _ surface.Dialect) (string, error) {
		matches := networkURIPattern.FindStringSubmatch(uri)
		if len(matches) != 2 {
			return "", fmt.Errorf("invalid URI format: %s", uri)
		}

		name := matches[1]

		// Try exact network match first
		if network, ok := client.GetNetwork(name); ok {
			inventoryURL := networkNodeInventoryURL(network)
			data, err := json.MarshalIndent(NetworkDetailResponse{
				ID:               name,
				ResourceURI:      "networks://" + name,
				NodeInventoryURL: inventoryURL,
				Usage:            networkDetailUsage(inventoryURL),
				Network:          network,
			}, "", "  ")
			if err != nil {
				return "", fmt.Errorf("marshaling response: %w", err)
			}

			return string(data), nil
		}

		// Try group match
		if networks, ok := client.GetGroup(name); ok {
			response := GroupDetailResponse{
				Group:    name,
				Networks: networks,
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
		sort.Strings(networkNames)

		matchingDisplayNames := matchingNetworkIDsByDisplayName(allNetworks, name)

		log.WithFields(logrus.Fields{
			"requested": name,
			"networks":  len(networkNames),
			"groups":    len(groups),
		}).Debug("Network or group not found")

		message := fmt.Sprintf("network or group %q not found", name)
		if len(matchingDisplayNames) > 0 {
			message += fmt.Sprintf(". Matching display name; use full network id: %s", strings.Join(matchingDisplayNames, ", "))
		}
		if len(groups) > 0 {
			message += fmt.Sprintf(". Available groups: %s", strings.Join(groups, ", "))
		}

		message += ". Read networks://active to list current ids."

		return "", errors.New(message)
	}
}

func networkNodeInventoryURL(network discovery.Network) string {
	if network.GenesisConfig == nil {
		return ""
	}

	for _, cfg := range network.GenesisConfig.API {
		if strings.Contains(cfg.Path, "/nodes/inventory") || strings.Contains(cfg.URL, "/nodes/inventory") {
			return cfg.URL
		}
	}

	return ""
}

func networkDetailUsage(inventoryURL string) string {
	if inventoryURL == "" {
		return "Use networks://active for current network ids. This network does not advertise a node inventory URL."
	}

	return "Use node_inventory_url to discover node instance labels for direct node API calls."
}

func matchingNetworkIDsByDisplayName(networks map[string]discovery.Network, displayName string) []string {
	var matches []string

	for id, network := range networks {
		if network.Name == displayName {
			matches = append(matches, id)
		}
	}

	sort.Strings(matches)

	return matches
}

func sortedGroupNames(groups map[string][]string) []string {
	names := make([]string, 0, len(groups))

	for name := range groups {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

func networkGroupIndex(groups map[string][]string) map[string]string {
	index := make(map[string]string)

	for group, networks := range groups {
		for _, network := range networks {
			index[network] = group
		}
	}

	return index
}
