package cli

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/spf13/cobra"
)

var networksDevnetsOnly bool

var networksCmd = &cobra.Command{
	GroupID: groupDiscovery,
	Use:     "networks",
	Aliases: []string{"network"},
	Short:   "List active networks from Cartographoor",
	Long: `List active Ethereum networks from the authoritative Cartographoor-backed
network inventory.

Examples:
  panda networks
  panda networks --devnets
  panda devnets
  panda devnet list
  panda networks -o json`,
	Args: cobra.NoArgs,
	RunE: runNetworks,
}

var networksListCmd = &cobra.Command{
	Use:   "list",
	Short: "List active networks",
	Args:  cobra.NoArgs,
	RunE:  runNetworks,
}

var devnetsCmd = &cobra.Command{
	GroupID: groupDiscovery,
	Use:     "devnets",
	Aliases: []string{"devnet"},
	Short:   "List active devnets from Cartographoor",
	Long: `List active devnet network ids from the authoritative Cartographoor-backed
network inventory.

Examples:
  panda devnets
  panda devnet list
  panda networks --devnets
  panda devnets -o json`,
	Args: cobra.NoArgs,
	RunE: runDevnets,
}

var devnetsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List active devnets",
	Args:  cobra.NoArgs,
	RunE:  runDevnets,
}

type activeNetworksResponse struct {
	Networks           []activeNetworkSummary `json:"networks"`
	Groups             []string               `json:"groups"`
	ActiveDevnetGroups map[string][]string    `json:"active_devnet_groups"`
	Usage              string                 `json:"usage"`
}

type activeNetworkSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ChainID     uint64 `json:"chain_id,omitempty"`
	Status      string `json:"status"`
	IsDevnet    bool   `json:"is_devnet"`
	DevnetGroup string `json:"devnet_group,omitempty"`
	ResourceURI string `json:"resource_uri"`
}

func init() {
	rootCmd.AddCommand(networksCmd)
	rootCmd.AddCommand(devnetsCmd)

	networksCmd.Flags().BoolVar(&networksDevnetsOnly, "devnets", false, "Only show active devnet networks")
	networksCmd.AddCommand(networksListCmd)
	devnetsCmd.AddCommand(devnetsListCmd)
}

func runNetworks(cmd *cobra.Command, _ []string) error {
	return runNetworksFiltered(cmd, networksDevnetsOnly)
}

func runDevnets(cmd *cobra.Command, _ []string) error {
	return runNetworksFiltered(cmd, true)
}

func runNetworksFiltered(cmd *cobra.Command, devnetsOnly bool) error {
	response, err := readResource(cmd.Context(), "networks://active")
	if err != nil {
		return fmt.Errorf("reading active networks: %w", err)
	}

	var payload activeNetworksResponse
	if err := json.Unmarshal([]byte(response.Content), &payload); err != nil {
		return fmt.Errorf("decoding active networks: %w", err)
	}

	if devnetsOnly {
		payload.Networks = filterDevnetNetworks(payload.Networks)
		payload.Groups = sortedActiveDevnetGroups(payload.ActiveDevnetGroups)
	}

	if isJSON() {
		return printJSON(payload)
	}

	return printNetworks(payload, devnetsOnly)
}

func printNetworks(response activeNetworksResponse, devnetsOnly bool) error {
	if len(response.Networks) == 0 {
		if devnetsOnly {
			fmt.Println("No active devnets found.")
		} else {
			fmt.Println("No active networks found.")
		}

		return nil
	}

	rows := make([][]string, 0, len(response.Networks))
	for _, network := range response.Networks {
		group := network.DevnetGroup
		if group == "" {
			group = "-"
		}

		chainID := ""
		if network.ChainID != 0 {
			chainID = fmt.Sprint(network.ChainID)
		}

		rows = append(rows, []string{
			network.ID,
			network.Name,
			network.Status,
			chainID,
			group,
		})
	}

	sort.Slice(rows, func(i, j int) bool {
		return rows[i][0] < rows[j][0]
	})

	printTable([]string{"ID", "NAME", "STATUS", "CHAIN_ID", "DEVNET_GROUP"}, rows)
	fmt.Println("\nSource: networks://active (Cartographoor)")

	return nil
}

func filterDevnetNetworks(networks []activeNetworkSummary) []activeNetworkSummary {
	filtered := make([]activeNetworkSummary, 0, len(networks))

	for _, network := range networks {
		if network.IsDevnet {
			filtered = append(filtered, network)
		}
	}

	return filtered
}

func sortedActiveDevnetGroups(groups map[string][]string) []string {
	names := make([]string, 0, len(groups))

	for name, networks := range groups {
		if len(networks) > 0 {
			names = append(names, name)
		}
	}

	sort.Strings(names)

	return names
}
