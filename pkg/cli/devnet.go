package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ethpandaops/panda/pkg/proxy/handlers"
)

var devnetCmd = &cobra.Command{
	GroupID: groupDirect,
	Use:     "devnet",
	Short:   "Call spamoor and assertoor APIs on a devnet through the proxy",
	Long: `Call devnet service APIs (spamoor transaction spammers, assertoor test runs)
through the proxy, which holds the credential the services require for writes.
No devnet cookie or token is needed; your account must be allowed for the
service on the proxy.

Paths are the service's own API routes:
  spamoor    /api/spammers, /api/spammer/{id}, /api/spammer/{id}/start|pause, /api/scenarios
  assertoor  /api/v1/tests, /api/v1/test_runs, /api/v1/test_run/{id}/status,
             /api/v1/test_runs/schedule, /api/v1/test_run/{id}/cancel

Examples:
  panda devnet networks
  panda devnet request glamsterdam-devnet-8 spamoor GET /api/spammers
  panda devnet request glamsterdam-devnet-8 spamoor POST /api/spammer/12/pause
  panda devnet request glamsterdam-devnet-8 assertoor POST /api/v1/test_runs/schedule \
    --data '{"test_id": "deploy-eip8282-contracts"}'`,
}

var (
	devnetRequestData  string
	devnetRequestQuery []string
)

var devnetNetworksCmd = &cobra.Command{
	Use:   "networks",
	Short: "List active networks running spamoor or assertoor",
	Args:  cobra.NoArgs,
	RunE:  runDevnetNetworks,
}

var devnetRequestCmd = &cobra.Command{
	Use:   "request <network> <service> <method> <path>",
	Short: "Call a spamoor or assertoor API path on a devnet",
	Args:  cobra.ExactArgs(4),
	ValidArgsFunction: func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		switch len(args) {
		case 1:
			return handlers.DevnetServiceNames, cobra.ShellCompDirectiveNoFileComp
		case 2:
			return []string{"GET", "POST", "PUT", "PATCH", "DELETE"}, cobra.ShellCompDirectiveNoFileComp
		default:
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
	},
	RunE: runDevnetRequest,
}

func init() {
	rootCmd.AddCommand(devnetCmd)
	devnetCmd.AddCommand(devnetNetworksCmd, devnetRequestCmd)

	devnetRequestCmd.Flags().StringVar(&devnetRequestData, "data", "", "JSON request body")
	devnetRequestCmd.Flags().StringArrayVar(&devnetRequestQuery, "query", nil, "query parameter key=value; repeatable")
}

func runDevnetNetworks(cmd *cobra.Command, _ []string) error {
	response, err := runServerOperation(cmd, "devnet.list_networks", nil)
	if err != nil {
		return err
	}

	if isJSON() {
		return printJSON(response.Data)
	}

	var payload struct {
		Networks []struct {
			Name  string `json:"name"`
			Extra struct {
				Services map[string]struct {
					URL     string `json:"url"`
					Relayed bool   `json:"relayed"`
				} `json:"services"`
			} `json:"extra"`
		} `json:"networks"`
	}

	data, err := json.Marshal(response.Data)
	if err != nil {
		return fmt.Errorf("encoding response: %w", err)
	}

	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("decoding devnet networks: %w", err)
	}

	if len(payload.Networks) == 0 {
		fmt.Println("No active networks run spamoor or assertoor.")

		return nil
	}

	for _, network := range payload.Networks {
		names := make([]string, 0, len(network.Extra.Services))
		for name := range network.Extra.Services {
			names = append(names, name)
		}

		sort.Strings(names)

		for _, name := range names {
			svc := network.Extra.Services[name]

			access := "read-only (not relayed for you)"
			if svc.Relayed {
				access = "relayed"
			}

			fmt.Printf("%-28s %-10s %-58s %s\n", network.Name, name, svc.URL, access)
		}
	}

	return nil
}

// devnetRequestArgs builds the devnet.request operation args from CLI input.
func devnetRequestArgs(args []string, data string, query []string) (map[string]any, error) {
	opArgs := map[string]any{
		"network": args[0],
		"service": args[1],
		"method":  strings.ToUpper(args[2]),
		"path":    args[3],
	}

	if data != "" {
		var body any
		if err := json.Unmarshal([]byte(data), &body); err != nil {
			return nil, fmt.Errorf("--data is not valid JSON: %w", err)
		}

		opArgs["body"] = body
	}

	if len(query) > 0 {
		params := make(map[string]any, len(query))
		for _, kv := range query {
			key, value, ok := strings.Cut(kv, "=")
			if !ok || key == "" {
				return nil, fmt.Errorf("--query %q must be key=value", kv)
			}

			params[key] = value
		}

		opArgs["query"] = params
	}

	return opArgs, nil
}

func runDevnetRequest(cmd *cobra.Command, args []string) error {
	opArgs, err := devnetRequestArgs(args, devnetRequestData, devnetRequestQuery)
	if err != nil {
		return err
	}

	response, err := runServerOperationRaw(cmd, "devnet.request", opArgs)
	if err != nil {
		return err
	}

	if strings.Contains(response.ContentType, "application/json") {
		return printJSONBytes(response.Body)
	}

	fmt.Println(string(response.Body))

	return nil
}
