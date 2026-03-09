package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/ethpandaops/mcp/pkg/resource"
)

var (
	doraJSON       bool
	doraHTTPClient = &http.Client{Timeout: 30 * time.Second}
)

var doraCmd = &cobra.Command{
	Use:   "dora",
	Short: "Query Dora beacon chain explorer",
	Long: `Query the Dora beacon chain explorer for network status, validators, and slots.

Examples:
  ep dora networks
  ep dora overview hoodi
  ep dora validator hoodi 12345
  ep dora slot hoodi 1000000
  ep dora epoch hoodi 100`,
}

func init() {
	rootCmd.AddCommand(doraCmd)
	doraCmd.PersistentFlags().BoolVar(&doraJSON, "json", false, "Output in JSON format")

	doraCmd.AddCommand(
		doraNetworksCmd,
		doraOverviewCmd,
		doraValidatorCmd,
		doraSlotCmd,
		doraEpochCmd,
	)
}

// startCartographoor creates and starts a cartographoor client.
func startCartographoor(ctx context.Context) (resource.CartographoorClient, func(), error) {
	client := resource.NewCartographoorClient(log, resource.CartographoorConfig{
		URL:      resource.DefaultCartographoorURL,
		CacheTTL: resource.DefaultCacheTTL,
		Timeout:  resource.DefaultHTTPTimeout,
	})

	if err := client.Start(ctx); err != nil {
		return nil, nil, fmt.Errorf("starting cartographoor client: %w", err)
	}

	cleanup := func() { _ = client.Stop() }

	return client, cleanup, nil
}

// getDoraNetworks returns a map of network name -> Dora URL from cartographoor.
func getDoraNetworks(client resource.CartographoorClient) map[string]string {
	networks := client.GetActiveNetworks()
	doraNetworks := make(map[string]string, len(networks))

	for name, network := range networks {
		if network.ServiceURLs != nil && network.ServiceURLs.Dora != "" {
			doraNetworks[name] = network.ServiceURLs.Dora
		}
	}

	return doraNetworks
}

// getDoraURL returns the Dora URL for a network or an error if not found.
func getDoraURL(client resource.CartographoorClient, network string) (string, error) {
	networks := getDoraNetworks(client)

	url, ok := networks[network]
	if !ok {
		available := make([]string, 0, len(networks))
		for name := range networks {
			available = append(available, name)
		}

		sort.Strings(available)

		return "", fmt.Errorf("unknown network %q. Available: %v", network, available)
	}

	return url, nil
}

// doraGet makes a GET request to a Dora explorer endpoint.
func doraGet(doraURL, path string) ([]byte, error) {
	resp, err := doraHTTPClient.Get(doraURL + path)
	if err != nil {
		return nil, fmt.Errorf("dora request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(data))
	}

	return data, nil
}

var doraNetworksCmd = &cobra.Command{
	Use:   "networks",
	Short: "List networks with Dora explorers",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		ctx := context.Background()

		client, cleanup, err := startCartographoor(ctx)
		if err != nil {
			return err
		}

		defer cleanup()

		networks := getDoraNetworks(client)

		if doraJSON {
			type networkInfo struct {
				Name    string `json:"name"`
				DoraURL string `json:"dora_url"`
			}

			result := make([]networkInfo, 0, len(networks))
			for name, url := range networks {
				result = append(result, networkInfo{Name: name, DoraURL: url})
			}

			sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })

			return printJSON(map[string]any{"networks": result, "total": len(result)})
		}

		if len(networks) == 0 {
			fmt.Println("No networks with Dora explorers found.")

			return nil
		}

		names := make([]string, 0, len(networks))
		for name := range networks {
			names = append(names, name)
		}

		sort.Strings(names)

		for _, name := range names {
			fmt.Printf("  %-30s  %s\n", name, networks[name])
		}

		return nil
	},
}

var doraOverviewCmd = &cobra.Command{
	Use:   "overview <network>",
	Short: "Get network overview",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		ctx := context.Background()

		client, cleanup, err := startCartographoor(ctx)
		if err != nil {
			return err
		}

		defer cleanup()

		doraURL, err := getDoraURL(client, args[0])
		if err != nil {
			return err
		}

		data, err := doraGet(doraURL, "/api/v1/epoch/head")
		if err != nil {
			// Fallback to /latest.
			data, err = doraGet(doraURL, "/api/v1/epoch/latest")
			if err != nil {
				return err
			}
		}

		if doraJSON {
			return printJSONBytes(data)
		}

		var resp struct {
			Data struct {
				Epoch                   json.Number `json:"epoch"`
				Finalized               bool        `json:"finalized"`
				GlobalParticipationRate float64     `json:"globalparticipationrate"`
				ValidatorInfo           *struct {
					Active  int `json:"active"`
					Total   int `json:"total"`
					Pending int `json:"pending"`
					Exited  int `json:"exited"`
				} `json:"validatorinfo"`
			} `json:"data"`
		}

		if err := json.Unmarshal(data, &resp); err != nil {
			return printJSONBytes(data)
		}

		fmt.Printf("Network:            %s\n", args[0])
		fmt.Printf("Current epoch:      %s\n", resp.Data.Epoch)
		fmt.Printf("Finalized:          %v\n", resp.Data.Finalized)
		fmt.Printf("Participation rate: %.2f\n", resp.Data.GlobalParticipationRate)

		if vi := resp.Data.ValidatorInfo; vi != nil {
			fmt.Printf("Active validators:  %d\n", vi.Active)
			fmt.Printf("Total validators:   %d\n", vi.Total)
			fmt.Printf("Pending validators: %d\n", vi.Pending)
			fmt.Printf("Exited validators:  %d\n", vi.Exited)
		}

		return nil
	},
}

var doraValidatorCmd = &cobra.Command{
	Use:   "validator <network> <index-or-pubkey>",
	Short: "Get validator details",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		ctx := context.Background()

		client, cleanup, err := startCartographoor(ctx)
		if err != nil {
			return err
		}

		defer cleanup()

		doraURL, err := getDoraURL(client, args[0])
		if err != nil {
			return err
		}

		data, err := doraGet(doraURL, fmt.Sprintf("/api/v1/validator/%s", args[1]))
		if err != nil {
			return err
		}

		return printJSONBytes(data)
	},
}

var doraSlotCmd = &cobra.Command{
	Use:   "slot <network> <slot-or-hash>",
	Short: "Get slot details",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		ctx := context.Background()

		client, cleanup, err := startCartographoor(ctx)
		if err != nil {
			return err
		}

		defer cleanup()

		doraURL, err := getDoraURL(client, args[0])
		if err != nil {
			return err
		}

		data, err := doraGet(doraURL, fmt.Sprintf("/api/v1/slot/%s", args[1]))
		if err != nil {
			return err
		}

		return printJSONBytes(data)
	},
}

var doraEpochCmd = &cobra.Command{
	Use:   "epoch <network> <epoch>",
	Short: "Get epoch summary",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		ctx := context.Background()

		client, cleanup, err := startCartographoor(ctx)
		if err != nil {
			return err
		}

		defer cleanup()

		doraURL, err := getDoraURL(client, args[0])
		if err != nil {
			return err
		}

		data, err := doraGet(doraURL, fmt.Sprintf("/api/v1/epoch/%s", args[1]))
		if err != nil {
			return err
		}

		return printJSONBytes(data)
	},
}
