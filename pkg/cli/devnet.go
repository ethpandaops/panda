package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/ethpandaops/panda/pkg/devnet"
	"github.com/ethpandaops/panda/pkg/operations"
)

var (
	devnetArgsFile    string
	devnetPackage     string
	devnetAlwaysPull  bool
	devnetDryRun      bool
	devnetDockerCache string
	devnetDownAll     bool
	devnetLogsTail    int
	devnetLogsFollow  bool
)

var devnetCmd = &cobra.Command{
	GroupID: groupWorkflow,
	Use:     "devnet",
	Short:   "Spin up Kurtosis Ethereum devnets",
	Long: `Spin up and manage multi-client Ethereum devnets as Kurtosis enclaves.

The panda server drives a Kurtosis engine (Docker or Kubernetes backend) to run
the ethpandaops ethereum-package; the CLI dispatches devnet operations to it, so
the server is what holds the cluster connection. The backend, package, and an
optional pull-through image cache are configured server-side under "devnet:":

  devnet:
    cluster: bruno                      # Kurtosis backend (docker | <k8s cluster>)
    package: github.com/ethpandaops/ethereum-package
    docker_cache: docker.ethquokkaops.io  # avoids Docker Hub rate limits

Debug a running devnet with the kurtosis CLI directly (the server already points
it at the right backend), e.g. ` + "`kurtosis service logs <enclave> <service> -f`" + `.

Examples:
  panda devnet up my-devnet --args ./network_params.yaml
  panda devnet ls
  panda devnet inspect my-devnet
  panda devnet down my-devnet`,
}

func init() {
	rootCmd.AddCommand(devnetCmd)

	devnetCmd.AddCommand(
		devnetUpCmd,
		devnetLsCmd,
		devnetInspectCmd,
		devnetServicesCmd,
		devnetEndpointsCmd,
		devnetLogsCmd,
		devnetDownCmd,
	)

	devnetUpCmd.Flags().StringVar(&devnetArgsFile, "args", "",
		"path to an ethereum-package args file (YAML or JSON); '-' reads stdin")
	devnetUpCmd.Flags().StringVar(&devnetPackage, "package", "",
		"Kurtosis package to run (overrides devnet.package in server config)")
	devnetUpCmd.Flags().BoolVar(&devnetAlwaysPull, "always-pull", false,
		"always re-pull images (use for devnet branches with mutable tags)")
	devnetUpCmd.Flags().BoolVar(&devnetDryRun, "dry-run", false,
		"validate and plan the run without applying it")
	devnetUpCmd.Flags().StringVar(&devnetDockerCache, "docker-cache", "",
		"pull-through registry cache host for all images (overrides devnet.docker_cache)")
	_ = devnetUpCmd.MarkFlagFilename("args", "yaml", "yml", "json")

	devnetDownCmd.Flags().BoolVar(&devnetDownAll, "all", false,
		"destroy every devnet enclave")

	devnetLogsCmd.Flags().IntVar(&devnetLogsTail, "tail", 0,
		"number of recent log lines per service (0 = server default)")
	devnetLogsCmd.Flags().BoolVarP(&devnetLogsFollow, "follow", "f", false,
		"stream logs live until interrupted (Ctrl-C)")

	devnetInspectCmd.ValidArgsFunction = completeEnclaveNames
	devnetServicesCmd.ValidArgsFunction = completeEnclaveNames
	devnetEndpointsCmd.ValidArgsFunction = completeEnclaveNames
	devnetLogsCmd.ValidArgsFunction = completeEnclaveNames
	devnetDownCmd.ValidArgsFunction = completeEnclaveNames
}

var devnetUpCmd = &cobra.Command{
	Use:   "up [enclave-name]",
	Short: "Create an enclave and launch a devnet",
	Long: `Create a Kurtosis enclave and run the ethereum-package in it.

If no enclave name is given, Kurtosis generates one. Package configuration is
read from the file passed with --args (the ethereum-package network_params
format); without it the package defaults are used.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		serializedArgs, err := readArgsFile(devnetArgsFile)
		if err != nil {
			return err
		}

		opArgs := map[string]any{}
		if len(args) == 1 {
			opArgs["name"] = args[0]
		}
		if serializedArgs != "" {
			opArgs["args"] = serializedArgs
		}
		if cmd.Flags().Changed("package") {
			opArgs["package"] = devnetPackage
		}
		if cmd.Flags().Changed("docker-cache") {
			opArgs["docker_cache"] = devnetDockerCache
		}
		if devnetAlwaysPull {
			opArgs["always_pull"] = true
		}
		if devnetDryRun {
			opArgs["dry_run"] = true
		}

		out := cmd.OutOrStdout()
		fmt.Fprintln(out, "Launching devnet (this can take a few minutes)…")

		resp, err := runServerOperation("devnet.up", opArgs)
		if err != nil {
			return err
		}

		var result struct {
			Enclave      string                    `json:"enclave"`
			Output       string                    `json:"output"`
			Error        string                    `json:"error"`
			Endpoints    []devnet.ServiceEndpoints `json:"endpoints"`
			IngressError string                    `json:"ingress_error"`
		}
		if err := decodeOperationData(resp, &result); err != nil {
			return err
		}

		if result.Output != "" {
			fmt.Fprint(out, result.Output)
		}

		if result.Error != "" {
			if result.Enclave != "" {
				fmt.Fprintf(out, "\nEnclave %q left in place; remove it with: panda devnet down %s\n", result.Enclave, result.Enclave)
			}
			return fmt.Errorf("%s", result.Error)
		}

		fmt.Fprintf(out, "\nDevnet %q is up.\n", result.Enclave)
		fmt.Fprintf(out, "  inspect:   panda devnet inspect %s\n", result.Enclave)
		fmt.Fprintf(out, "  services:  panda devnet services %s\n", result.Enclave)
		fmt.Fprintf(out, "  logs:      panda devnet logs %s [service] [-f]\n", result.Enclave)
		fmt.Fprintf(out, "  destroy:   panda devnet down %s\n", result.Enclave)

		if len(result.Endpoints) > 0 {
			fmt.Fprintln(out, "\nendpoints:")
			for _, e := range result.Endpoints {
				if e.PrimaryURL != "" {
					fmt.Fprintf(out, "  %-28s %s\n", e.Service, e.PrimaryURL)
				}
			}
		}
		if result.IngressError != "" {
			fmt.Fprintf(out, "\n(ingress not fully configured: %s)\n", result.IngressError)
		}

		return nil
	},
}

var devnetServicesCmd = &cobra.Command{
	Use:   "services <enclave>",
	Short: "List services running in a devnet",
	Long: `List the services (EL/CL/VC clients and tools) running in a devnet enclave.

The names shown are what 'panda devnet logs' accepts to select services.`,
	Args: cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		resp, err := runServerOperation("devnet.services", map[string]any{"enclave": args[0]})
		if err != nil {
			return err
		}

		var svcs []devnet.Service
		if err := decodeOperationData(resp, &svcs); err != nil {
			return err
		}

		if isJSON() {
			return printJSON(svcs)
		}

		if len(svcs) == 0 {
			fmt.Println("No services found.")
			return nil
		}

		rows := make([][]string, 0, len(svcs))
		for _, s := range svcs {
			rows = append(rows, []string{s.Name, formatPorts(s.Ports), s.PrivateIP})
		}
		printTable([]string{"SERVICE", "PORTS", "PRIVATE IP"}, rows)

		return nil
	},
}

// formatPorts renders a service's ports compactly as "name:number" entries,
// e.g. "rpc:8545 ws:8546 engine-rpc:8551".
func formatPorts(ports []devnet.Port) string {
	if len(ports) == 0 {
		return "-"
	}

	parts := make([]string, 0, len(ports))
	for _, p := range ports {
		parts = append(parts, fmt.Sprintf("%s:%d", p.Name, p.Number))
	}

	return strings.Join(parts, " ")
}

var devnetEndpointsCmd = &cobra.Command{
	Use:   "endpoints <enclave>",
	Short: "Show external URLs for a devnet's services",
	Long: `Show the stable external URLs panda assigns to a devnet's services.

Each exposed service is reachable at an owner-scoped hostname; this lists the
primary URL per service (the dora UI and EL rpc are the headline ones). Pass
--json for the full per-port detail.`,
	Args: cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		resp, err := runServerOperation("devnet.endpoints", map[string]any{"enclave": args[0]})
		if err != nil {
			return err
		}

		var eps []devnet.ServiceEndpoints
		if err := decodeOperationData(resp, &eps); err != nil {
			return err
		}

		if isJSON() {
			return printJSON(eps)
		}

		if len(eps) == 0 {
			fmt.Println("No exposed services found.")
			return nil
		}

		rows := make([][]string, 0, len(eps))
		for _, e := range eps {
			rows = append(rows, []string{e.Service, e.PrimaryURL})
		}
		printTable([]string{"SERVICE", "URL"}, rows)

		return nil
	},
}

var devnetLogsCmd = &cobra.Command{
	Use:   "logs <enclave> [service...]",
	Short: "Show recent logs for devnet services",
	Long: `Fetch recent logs for services in a devnet enclave.

With no service names, logs for every service are returned. Each line is
prefixed with its service name. Use 'panda devnet services <enclave>' to see
the available service names.

Logs are fetched through the panda server (which holds the cluster connection),
so this works wherever 'panda devnet ls' works — including remotely through the
cloud proxy — without needing the kurtosis CLI or a gateway locally.`,
	Example: `  panda devnet logs my-devnet
  panda devnet logs my-devnet el-1-geth-lighthouse cl-1-lighthouse-geth
  panda devnet logs my-devnet el-1-geth-lighthouse --tail 500`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if devnetLogsFollow {
			return followDevnetLogs(cmd, args[0], args[1:])
		}

		opArgs := map[string]any{"enclave": args[0]}
		if len(args) > 1 {
			services := make([]any, 0, len(args)-1)
			for _, s := range args[1:] {
				services = append(services, s)
			}
			opArgs["services"] = services
		}
		if cmd.Flags().Changed("tail") {
			opArgs["tail"] = devnetLogsTail
		}

		resp, err := runServerOperation("devnet.logs", opArgs)
		if err != nil {
			return err
		}

		var result struct {
			Logs string `json:"logs"`
		}
		if err := decodeOperationData(resp, &result); err != nil {
			return err
		}

		fmt.Fprint(cmd.OutOrStdout(), result.Logs)

		return nil
	},
}

// followDevnetLogs streams logs live from the server's streaming endpoint until
// the user interrupts (Ctrl-C). It uses a raw GET (not the JSON operation path)
// because the response is an open-ended chunked text stream.
func followDevnetLogs(cmd *cobra.Command, enclave string, serviceNames []string) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	query := url.Values{"enclave": []string{enclave}}
	for _, name := range serviceNames {
		query.Add("service", name)
	}
	if cmd.Flags().Changed("tail") {
		query.Set("tail", strconv.Itoa(devnetLogsTail))
	}

	return serverStreamGet(ctx, "/api/v1/devnet/logs", query, cmd.OutOrStdout())
}

var devnetLsCmd = &cobra.Command{
	Use:     "ls",
	Aliases: []string{"list"},
	Short:   "List devnet enclaves",
	Args:    cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		enclaves, err := fetchEnclaves()
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSON(enclaves)
		}

		if len(enclaves) == 0 {
			fmt.Println("No devnets found.")
			return nil
		}

		rows := make([][]string, 0, len(enclaves))
		for _, e := range enclaves {
			rows = append(rows, []string{
				e.Name,
				e.Status,
				e.APIContainer,
				shortUUID(e.UUID),
				formatCreated(e.CreationTime),
			})
		}
		printTable([]string{"NAME", "STATUS", "API CONTAINER", "UUID", "CREATED"}, rows)

		return nil
	},
}

var devnetInspectCmd = &cobra.Command{
	Use:   "inspect <enclave>",
	Short: "Show details for a devnet enclave",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		resp, err := runServerOperation("devnet.inspect", map[string]any{"enclave": args[0]})
		if err != nil {
			return err
		}

		var enclave devnet.Enclave
		if err := decodeOperationData(resp, &enclave); err != nil {
			return err
		}

		if isJSON() {
			return printJSON(enclave)
		}

		printTable(
			[]string{"FIELD", "VALUE"},
			[][]string{
				{"Name", enclave.Name},
				{"UUID", enclave.UUID},
				{"Status", enclave.Status},
				{"API container", enclave.APIContainer},
				{"Created", formatCreated(enclave.CreationTime)},
			},
		)

		return nil
	},
}

var devnetDownCmd = &cobra.Command{
	Use:     "down [enclave]",
	Aliases: []string{"rm", "destroy"},
	Short:   "Destroy a devnet enclave (or all with --all)",
	Long: `Destroy a devnet enclave, tearing down its namespace, pods and volumes.

Pass an enclave name to destroy one, or --all to prune every devnet — useful for
reclaiming cluster resources when no devnets are needed anymore.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		opArgs := map[string]any{}
		switch {
		case devnetDownAll:
			if len(args) != 0 {
				return fmt.Errorf("--all takes no enclave name")
			}
			opArgs["all"] = true
		case len(args) == 1:
			opArgs["enclave"] = args[0]
		default:
			return fmt.Errorf("requires an enclave name, or --all to destroy every devnet")
		}

		resp, err := runServerOperation("devnet.down", opArgs)
		if err != nil {
			return err
		}

		var result struct {
			Destroyed []string `json:"destroyed"`
		}
		if err := decodeOperationData(resp, &result); err != nil {
			return err
		}

		if len(result.Destroyed) == 0 {
			fmt.Println("No devnets to destroy.")
			return nil
		}
		for _, name := range result.Destroyed {
			fmt.Printf("Destroyed devnet %q.\n", name)
		}

		return nil
	},
}

// decodeOperationData re-decodes the generic operation Data payload into a typed
// target via a JSON round-trip.
func decodeOperationData(resp *operations.Response, target any) error {
	raw, err := json.Marshal(resp.Data)
	if err != nil {
		return fmt.Errorf("encoding response data: %w", err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("decoding response data: %w", err)
	}

	return nil
}

// fetchEnclaves lists enclaves via the server.
func fetchEnclaves() ([]devnet.Enclave, error) {
	resp, err := runServerOperation("devnet.ls", map[string]any{})
	if err != nil {
		return nil, err
	}

	var enclaves []devnet.Enclave
	if err := decodeOperationData(resp, &enclaves); err != nil {
		return nil, err
	}

	return enclaves, nil
}

// readArgsFile reads ethereum-package args from path. An empty path means no
// args (package defaults); "-" reads from stdin.
func readArgsFile(path string) (string, error) {
	if path == "" {
		return "", nil
	}

	var (
		data []byte
		err  error
	)
	if path == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return "", fmt.Errorf("reading args file %q: %w", path, err)
	}

	return string(data), nil
}

// completeEnclaveNames provides shell completion of existing enclave names.
func completeEnclaveNames(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	enclaves, err := fetchEnclaves()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	names := make([]string, 0, len(enclaves))
	for _, e := range enclaves {
		names = append(names, e.Name)
	}

	return names, cobra.ShellCompDirectiveNoFileComp
}

func shortUUID(uuid string) string {
	const shortLen = 12
	if len(uuid) <= shortLen {
		return uuid
	}

	return uuid[:shortLen]
}

func formatCreated(t time.Time) string {
	if t.IsZero() {
		return "-"
	}

	return t.Local().Format("2006-01-02 15:04:05")
}
