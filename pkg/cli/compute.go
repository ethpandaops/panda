package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// computeProbeTimeout bounds the help-time datasource probe that decides
// whether the compute command group is advertised.
const computeProbeTimeout = 2 * time.Second

var (
	computeDatasource   string
	computeTTL          string
	computeOnDelete     string
	computeNote         string
	computeExtend       string
	computeTemplate     string
	computeName         string
	computeVersion      string
	computeDescription  string
	computeReplace      bool
	computePublicKey    string
	computeIdempotency  string
	computeLimit        int
	computeOffset       int
	computeCursor       string
	computeFilters      []string
	computeTags         []string
	computeSnapshotID   string
	computeBootFlavor   string
	computeVCPU         int
	computeMemoryMB     int
	computeDiskGB       int
	computeEnvValues    []string
	computeLabelValues  []string
	computeHooksJSON    string
	computeWatchdogJSON string
	computeExecTimeout  string
	computeLogSource    string
	computeLogTail      int
	computePaused       bool
	computeForkCount    int
	computeForkRNG      string
	computeForkClock    string
	computeForkMinReady int
	computeForkDeadline string
	computeForkFlavor   string
	computeForkPaused   bool
	computePortProtocol string
	computePortService  string
)

var computeCmd = &cobra.Command{
	GroupID: groupDirect,
	Use:     "compute <resource> <command>",
	Short:   "Manage ephemeral compute sandboxes: create, snapshot, fork, lease",
	Long: `Manage compute, the ethpandaops ephemeral-sandbox control plane. A sandbox is
a short-lived microVM created from a template; you can snapshot it, stop and
start it, extend its lease (TTL), create new sandboxes from snapshots, fan out
copies with fork, and poll the async operations that back every mutation.

Commands are grouped by resource. Most mutations are asynchronous: they return
an operation id you can poll with 'panda compute operations get <id>'.

Access is restricted to core ethpandaops members. The command is only shown
when a compute datasource is reachable through the configured proxies.

The --datasource flag can be omitted when a single compute datasource is
configured.

Examples:
  panda compute datasources
  panda compute images list
  panda compute sandboxes create --template ubuntu/24.04 --ttl 1h
  panda compute sandboxes list
  panda compute sandboxes get <id>
  panda compute sandboxes snapshot <id> --note "before upgrade"
  panda compute sandboxes lease <id> --extend 30m
  panda compute sandboxes create --snapshot <snapshot_id> --ttl 2h
  panda compute sandboxes fork <id> --count 5 --ttl 1h --identity-rng reseed --identity-clock correct
  panda compute images fork <image_id> --count 5 --ttl 1h --identity-rng reseed --identity-clock correct
  panda compute forks get <fork_id>
  panda compute operations get <id>
  panda compute sandboxes delete <id>`,
}

func init() {
	rootCmd.AddCommand(computeCmd)

	// Hidden until a help-time probe confirms a compute datasource is
	// reachable; see registerComputeVisibility.
	computeCmd.Hidden = true

	computeCmd.PersistentFlags().StringVar(&computeDatasource, "datasource", "",
		"Compute datasource name (optional when only one is configured)")
	_ = computeCmd.RegisterFlagCompletionFunc("datasource", func(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return completeDatasourceNames("compute")(cmd, nil, toComplete)
	})

	// List flags shared by the per-resource `list` commands.
	for _, cmd := range []*cobra.Command{
		computeSandboxesListCmd, computeImagesListCmd, computeBakesListCmd,
		computeOperationsListCmd, computeKeysListCmd,
	} {
		cmd.Flags().IntVar(&computeLimit, "limit", 0, "Maximum items to return")
		cmd.Flags().IntVar(&computeOffset, "offset", 0, "Items to skip")
		cmd.Flags().StringVar(&computeCursor, "cursor", "", "Pagination cursor (next_cursor from a prior page)")
		cmd.Flags().StringArrayVar(&computeFilters, "filter", nil,
			"Filter results, key<op>value where op is =, !=, ~=, >, <, >=, <= (e.g. state=running); repeatable")
	}

	computeSandboxesCreateCmd.Flags().StringVar(&computeTemplate, "template", "", "Template to launch")
	computeSandboxesCreateCmd.Flags().StringVar(&computeSnapshotID, "snapshot", "", "Snapshot to boot from instead of a template")
	computeSandboxesCreateCmd.Flags().StringVar(&computeBootFlavor, "boot-flavor", "",
		"Snapshot boot flavor: warm (resume memory, default) or cold (fresh boot on the snapshot disk)")
	computeSandboxesCreateCmd.Flags().StringVar(&computeTTL, "ttl", "", "Lease duration (Go duration, e.g. 1h)")
	computeSandboxesCreateCmd.Flags().StringVar(&computeOnDelete, "on-delete", "",
		"Disposition on delete: archive, delete, or hot")
	computeSandboxesCreateCmd.Flags().StringVar(&computeName, "name", "", "Display name for the sandbox")
	computeSandboxesCreateCmd.Flags().IntVar(&computeVCPU, "vcpu", 0, "vCPU override")
	computeSandboxesCreateCmd.Flags().IntVar(&computeMemoryMB, "memory-mb", 0, "Memory override in MiB")
	computeSandboxesCreateCmd.Flags().IntVar(&computeDiskGB, "disk-gb", 0, "Disk override in GiB")
	computeSandboxesCreateCmd.Flags().StringArrayVar(&computeEnvValues, "env", nil, "Guest environment KEY=VALUE; repeatable")
	computeSandboxesCreateCmd.Flags().StringArrayVar(&computeLabelValues, "label", nil, "Metadata label KEY=VALUE; repeatable")
	computeSandboxesCreateCmd.Flags().StringVar(&computeHooksJSON, "hooks-json", "",
		"Lifecycle hooks as a JSON array of hook declarations")
	computeSandboxesCreateCmd.Flags().StringVar(&computeWatchdogJSON, "watchdog-json", "",
		"Watchdog declaration as a JSON object")
	computeSandboxesCreateCmd.Flags().BoolVar(&computePaused, "paused", false,
		"Leave the sandbox paused after a warm snapshot boot instead of running")

	for _, cmd := range []*cobra.Command{computeSandboxesForkCmd, computeImagesForkCmd} {
		cmd.Flags().IntVar(&computeForkCount, "count", 0, "Number of sandboxes to create (required)")
		_ = cmd.MarkFlagRequired("count")
		cmd.Flags().StringVar(&computeForkRNG, "identity-rng", "",
			"Child RNG policy: reseed (required; inherit reserved for a future firecracker build)")
		_ = cmd.MarkFlagRequired("identity-rng")
		cmd.Flags().StringVar(&computeForkClock, "identity-clock", "",
			"Child clock policy: correct (step to real time at resume) or inherit (snapshot policy) (required)")
		_ = cmd.MarkFlagRequired("identity-clock")
		cmd.Flags().StringVar(&computeTTL, "ttl", "",
			"Lease duration applied to every child (Go duration; omit for the server default)")
		cmd.Flags().IntVar(&computeForkMinReady, "min-ready", 0,
			"Floor of ready children below which the fork reports failure")
		cmd.Flags().StringVar(&computeForkDeadline, "deadline", "",
			"How long queued children may wait for capacity (Go duration)")
		cmd.Flags().StringVar(&computeForkFlavor, "flavor", "",
			"Child boot flavor: warm (resume memory, default) or cold (fresh boot on the snapshot disk)")
		cmd.Flags().BoolVar(&computeForkPaused, "paused", false,
			"Whether children land paused instead of running (omit to inherit the source default)")
	}

	computeSandboxesExecCmd.Flags().StringVar(&computeExecTimeout, "timeout", "",
		"Command timeout (Go duration, server default 30s, max 5m)")
	computeSandboxesLogsCmd.Flags().StringVar(&computeLogSource, "source", "", "Restrict logs to one source: console or firecracker")
	computeSandboxesLogsCmd.Flags().IntVar(&computeLogTail, "tail-bytes", 0, "Per-source byte tail to return")

	computeSandboxesSnapshotCmd.Flags().StringVar(&computeNote, "note", "", "Optional note recorded with the snapshot")
	computeSandboxesSnapshotCmd.Flags().StringVar(&computeTTL, "ttl", "",
		"Snapshot lifetime (Go duration; \"0\" means no expiry, omit for the server default)")
	computeSandboxesLeaseCmd.Flags().StringVar(&computeExtend, "extend", "",
		"Lease extension (Go duration, e.g. 30m) (required)")
	_ = computeSandboxesLeaseCmd.MarkFlagRequired("extend")
	computeImagesPromoteCmd.Flags().StringVar(&computeName, "name", "", "Named image name (required)")
	computeImagesPromoteCmd.Flags().StringVar(&computeVersion, "version", "", "Named image version")
	computeImagesPromoteCmd.Flags().StringVar(&computeDescription, "description", "", "Named image description")
	computeImagesPromoteCmd.Flags().BoolVar(&computeReplace, "replace", false, "Replace an existing image version")
	computeImagesPromoteCmd.Flags().StringArrayVar(&computeTags, "tags", nil, "Named image tag; repeatable")
	_ = computeImagesPromoteCmd.MarkFlagRequired("name")

	computeSandboxesExposeCmd.Flags().StringVar(&computeName, "name", "", "Display name for the exposed port")
	computeSandboxesExposeCmd.Flags().StringVar(&computePortProtocol, "protocol", "", "Port protocol (e.g. http)")
	computeSandboxesExposeCmd.Flags().StringVar(&computePortService, "service", "", "Service label for the exposed port")

	computeKeysAddCmd.Flags().StringVar(&computePublicKey, "public-key", "", "SSH public key material (required)")
	computeKeysAddCmd.Flags().StringVar(&computeName, "name", "", "Optional label for the key")
	_ = computeKeysAddCmd.MarkFlagRequired("public-key")

	computeSandboxesSSHCmd.Flags().StringVar(&computeSSHIdentity, "identity", "~/.ssh/id_ed25519",
		"Private key whose public half is registered with 'panda compute keys add'")
	computeSandboxesSSHCmd.Flags().BoolVar(&computeSSHPrint, "print", false,
		"Print the ssh command instead of executing it")

	for _, cmd := range []*cobra.Command{
		computeSandboxesCreateCmd, computeSandboxesDeleteCmd, computeSandboxesStopCmd,
		computeSandboxesStartCmd, computeSandboxesSnapshotCmd, computeSandboxesPauseCmd,
		computeSandboxesResumeCmd, computeSandboxesForkCmd, computeSandboxesExposeCmd,
		computeSandboxesUnexposeCmd, computeImagesDeleteCmd, computeImagesForkCmd,
		computeImagesPromoteCmd, computeImagesDeactivateCmd, computeBakesRunCmd,
	} {
		cmd.Flags().StringVar(&computeIdempotency, "idempotency-key", "",
			"Idempotency key to make the mutation safely retryable")
	}

	computeSandboxesCmd.AddCommand(
		computeSandboxesListCmd, computeSandboxesGetCmd, computeSandboxesCreateCmd,
		computeSandboxesDeleteCmd, computeSandboxesStopCmd, computeSandboxesStartCmd,
		computeSandboxesSnapshotCmd, computeSandboxesLeaseCmd, computeSandboxesImagesCmd,
		computeSandboxesOperationsCmd, computeSandboxesLogsCmd, computeSandboxesLineageCmd,
		computeSandboxesExecCmd, computeSandboxesMetricsCmd, computeSandboxesPauseCmd,
		computeSandboxesResumeCmd, computeSandboxesHooksCmd, computeSandboxesHookRunsCmd,
		computeSandboxesForkCmd, computeSandboxesSSHCmd, computeSandboxesExposeCmd,
		computeSandboxesUnexposeCmd,
	)
	computeImagesCmd.AddCommand(
		computeImagesListCmd, computeImagesGetCmd, computeImagesDeleteCmd,
		computeImagesForkCmd, computeImagesPromoteCmd, computeImagesDeactivateCmd,
		computeImagesChildrenCmd, computeImagesLineageCmd,
	)
	computeForksCmd.AddCommand(computeForksListCmd, computeForksGetCmd)
	computeBakesCmd.AddCommand(computeBakesListCmd, computeBakesRunCmd)
	computeOperationsCmd.AddCommand(computeOperationsListCmd, computeOperationsGetCmd)
	computeKeysCmd.AddCommand(computeKeysListCmd, computeKeysAddCmd, computeKeysDeleteCmd)
	computeUsersCmd.AddCommand(computeUsersListCmd, computeUsersGetCmd)
	computeNodesCmd.AddCommand(computeNodesListCmd, computeNodesGetCmd)

	computeCmd.AddCommand(
		computeDatasourcesCmd,
		computeMetaCmd,
		computeAuditCmd,
		computeSessionCmd,
		computeSandboxesCmd,
		computeImagesCmd,
		computeForksCmd,
		computeBakesCmd,
		computeOperationsCmd,
		computeKeysCmd,
		computeUsersCmd,
		computeNodesCmd,
	)

	registerComputeVisibility()
}

// registerComputeVisibility wraps the root help function so the compute command
// is revealed only when a compute datasource is reachable. Execution is never
// blocked — a hidden command still runs — so this controls advertisement only.
func registerComputeVisibility() {
	defaultHelp := rootCmd.HelpFunc()

	rootCmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		if cmd == rootCmd || commandInComputeTree(cmd) {
			computeCmd.Hidden = !computeAvailable(cmd.Context())
		}

		defaultHelp(cmd, args)
	})
}

// commandInComputeTree reports whether cmd is the compute command or one of its
// descendants.
func commandInComputeTree(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if c == computeCmd {
			return true
		}
	}

	return false
}

// computeAvailable reports whether the server advertises at least one compute
// datasource. It fails safe (returns false) on any error so the command stays
// hidden when the server is unreachable.
func computeAvailable(ctx context.Context) bool {
	if ctx == nil {
		ctx = context.Background()
	}

	probeCtx, cancel := context.WithTimeout(ctx, computeProbeTimeout)
	defer cancel()

	response, err := listDatasources(probeCtx, "compute")
	if err != nil {
		return false
	}

	return len(response.Datasources) > 0
}

// Resource groups.

var computeSandboxesCmd = &cobra.Command{
	Use:   "sandboxes <command>",
	Short: "Create, inspect, and control sandboxes",
}

var computeImagesCmd = &cobra.Command{
	Use:   "images <command>",
	Short: "List, fork, promote, and delete bootable images",
	Long: `Manage bootable images. An image is either raw (a snapshot captured from a
sandbox, addressed by snapshot id) or named (a published name@version). Promote
turns a raw image into a named one; deactivate retires a named image.`,
}

var computeForksCmd = &cobra.Command{
	Use:   "forks <command>",
	Short: "Inspect fork operations and their per-child progress",
}

var computeBakesCmd = &cobra.Command{
	Use:   "bakes <command>",
	Short: "Inspect and trigger scheduled image bakes",
}

var computeOperationsCmd = &cobra.Command{
	Use:   "operations <command>",
	Short: "Inspect async operations",
}

var computeKeysCmd = &cobra.Command{
	Use:   "keys <command>",
	Short: "Manage your SSH public keys",
}

var computeUsersCmd = &cobra.Command{
	Use:   "users <command>",
	Short: "Inspect the user directory",
}

var computeNodesCmd = &cobra.Command{
	Use:   "nodes <command>",
	Short: "Inspect compute nodes",
}

// Top-level leaves.

var computeDatasourcesCmd = &cobra.Command{
	Use:   "datasources",
	Short: "List compute datasources",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		response, err := runServerOperation(cmd, "compute.list_datasources", map[string]any{})
		if err != nil {
			return err
		}

		return printListing(response, "datasources", "No compute datasources found.")
	},
}

var computeMetaCmd = &cobra.Command{
	Use:   "meta",
	Short: "Show compute service metadata",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runComputeRaw(cmd, "compute.meta", computeArgs())
	},
}

var computeAuditCmd = &cobra.Command{
	Use:   "audit",
	Short: "List audit log entries",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runComputeRaw(cmd, "compute.list_audit", computeArgs())
	},
}

var computeSessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Show the authenticated session and identity",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runComputeRaw(cmd, "compute.auth_session", computeArgs())
	},
}

// Sandboxes.

var computeSandboxesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List sandboxes",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runComputeRaw(cmd, "compute.list_sandboxes", computeListArgs())
	},
}

var computeSandboxesGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Get one sandbox by id",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runComputeRaw(cmd, "compute.get_sandbox", computeIDArgs(args[0]))
	},
}

var computeSandboxesCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a sandbox from a template or snapshot (async)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if (computeTemplate == "") == (computeSnapshotID == "") {
			return fmt.Errorf("exactly one of --template or --snapshot is required")
		}
		opArgs := computeArgs()
		setIfNotEmpty(opArgs, "template", computeTemplate)
		setIfNotEmpty(opArgs, "snapshot_id", computeSnapshotID)
		setIfNotEmpty(opArgs, "flavor", computeBootFlavor)
		setIfNotEmpty(opArgs, "ttl", computeTTL)
		setIfNotEmpty(opArgs, "on_delete", computeOnDelete)
		opArgs["idempotency_key"] = computeIdemOrGenerated()
		setIfNotEmpty(opArgs, "name", computeName)
		if computeVCPU > 0 {
			opArgs["vcpu"] = computeVCPU
		}
		if computeMemoryMB > 0 {
			opArgs["memory_mb"] = computeMemoryMB
		}
		if computeDiskGB > 0 {
			opArgs["disk_gb"] = computeDiskGB
		}
		if env, err := keyValueArgsToMap(computeEnvValues); err != nil {
			return fmt.Errorf("--env: %w", err)
		} else if len(env) > 0 {
			opArgs["env"] = env
		}
		if labels, err := keyValueArgsToMap(computeLabelValues); err != nil {
			return fmt.Errorf("--label: %w", err)
		} else if len(labels) > 0 {
			opArgs["labels"] = labels
		}
		if computeHooksJSON != "" {
			var hooks []any
			if err := json.Unmarshal([]byte(computeHooksJSON), &hooks); err != nil {
				return fmt.Errorf("--hooks-json: %w", err)
			}
			opArgs["hooks"] = hooks
		}
		if computeWatchdogJSON != "" {
			var watchdog map[string]any
			if err := json.Unmarshal([]byte(computeWatchdogJSON), &watchdog); err != nil {
				return fmt.Errorf("--watchdog-json: %w", err)
			}
			opArgs["watchdog"] = watchdog
		}
		if cmd.Flags().Changed("paused") {
			opArgs["paused"] = computePaused
		}

		return runComputeRaw(cmd, "compute.create_sandbox", opArgs)
	},
}

var computeSandboxesDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a sandbox (async)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runComputeRaw(cmd, "compute.delete_sandbox", computeMutationArgs(args[0]))
	},
}

var computeSandboxesStopCmd = &cobra.Command{
	Use:   "stop <id>",
	Short: "Stop a running sandbox (async)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runComputeRaw(cmd, "compute.stop_sandbox", computeMutationArgs(args[0]))
	},
}

var computeSandboxesStartCmd = &cobra.Command{
	Use:   "start <id>",
	Short: "Start a stopped sandbox (async)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runComputeRaw(cmd, "compute.start_sandbox", computeMutationArgs(args[0]))
	},
}

var computeSandboxesSnapshotCmd = &cobra.Command{
	Use:   "snapshot <id>",
	Short: "Snapshot a sandbox (async)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		opArgs := computeMutationArgs(args[0])
		setIfNotEmpty(opArgs, "note", computeNote)
		setIfNotEmpty(opArgs, "ttl", computeTTL)

		return runComputeRaw(cmd, "compute.snapshot_sandbox", opArgs)
	},
}

var computeSandboxesLeaseCmd = &cobra.Command{
	Use:   "lease <id>",
	Short: "Extend a sandbox lease (TTL)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		opArgs := computeIDArgs(args[0])
		opArgs["extend"] = computeExtend

		return runComputeRaw(cmd, "compute.lease_sandbox", opArgs)
	},
}

var computeSandboxesImagesCmd = &cobra.Command{
	Use:   "images <id>",
	Short: "List raw images captured from a sandbox",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runComputeRaw(cmd, "compute.get_sandbox_images", computeIDArgs(args[0]))
	},
}

var computeSandboxesOperationsCmd = &cobra.Command{
	Use:   "operations <id>",
	Short: "List operations for a sandbox",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runComputeRaw(cmd, "compute.get_sandbox_operations", computeIDArgs(args[0]))
	},
}

var computeSandboxesLogsCmd = &cobra.Command{
	Use:   "logs <id>",
	Short: "Fetch logs for a sandbox",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		opArgs := computeIDArgs(args[0])
		setIfNotEmpty(opArgs, "source", computeLogSource)
		if computeLogTail > 0 {
			opArgs["tail_bytes"] = computeLogTail
		}

		return runComputeRaw(cmd, "compute.get_sandbox_logs", opArgs)
	},
}

var computeSandboxesLineageCmd = &cobra.Command{
	Use:   "lineage <id>",
	Short: "Show the snapshot/restore lineage of a sandbox",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runComputeRaw(cmd, "compute.get_sandbox_lineage", computeIDArgs(args[0]))
	},
}

var computeSandboxesExecCmd = &cobra.Command{
	Use:   "exec <id> -- <command> [args...]",
	Short: "Run a command inside a sandbox and print its output",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		opArgs := computeIDArgs(args[0])
		command := make([]any, 0, len(args)-1)
		for _, arg := range args[1:] {
			command = append(command, arg)
		}
		opArgs["command"] = command
		setIfNotEmpty(opArgs, "timeout", computeExecTimeout)

		return runComputeRaw(cmd, "compute.exec_sandbox", opArgs)
	},
}

var computeSandboxesMetricsCmd = &cobra.Command{
	Use:   "metrics <id>",
	Short: "Fetch guest resource metrics for a sandbox",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runComputeRaw(cmd, "compute.get_sandbox_metrics", computeIDArgs(args[0]))
	},
}

var computeSandboxesPauseCmd = &cobra.Command{
	Use:   "pause <id>",
	Short: "Pause a running sandbox's vCPUs (async)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runComputeRaw(cmd, "compute.pause_sandbox", computeMutationArgs(args[0]))
	},
}

var computeSandboxesResumeCmd = &cobra.Command{
	Use:   "resume <id>",
	Short: "Resume a paused sandbox (async)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runComputeRaw(cmd, "compute.resume_sandbox", computeMutationArgs(args[0]))
	},
}

var computeSandboxesHooksCmd = &cobra.Command{
	Use:   "hooks <id>",
	Short: "List lifecycle hooks declared on a sandbox",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runComputeRaw(cmd, "compute.get_sandbox_hooks", computeIDArgs(args[0]))
	},
}

var computeSandboxesHookRunsCmd = &cobra.Command{
	Use:   "hook-runs <id>",
	Short: "List lifecycle hook executions for a sandbox",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runComputeRaw(cmd, "compute.get_sandbox_hook_runs", computeIDArgs(args[0]))
	},
}

var computeSandboxesForkCmd = &cobra.Command{
	Use:   "fork <id>",
	Short: "Fan out copies of a running sandbox (async)",
	Long: `Capture the sandbox as an ephemeral snapshot and fan out --count sandboxes
from it. The source keeps running.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runComputeRaw(cmd, "compute.fork_sandbox", computeForkArgs(cmd, args[0]))
	},
}

var computeSandboxesExposeCmd = &cobra.Command{
	Use:   "expose <id> <port>",
	Short: "Expose a sandbox port through the ingress gateway",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		port, err := strconv.Atoi(args[1])
		if err != nil || port < 1 {
			return fmt.Errorf("port must be a positive integer, got %q", args[1])
		}
		opArgs := computeMutationArgs(args[0])
		opArgs["port"] = port
		setIfNotEmpty(opArgs, "name", computeName)
		setIfNotEmpty(opArgs, "protocol", computePortProtocol)
		setIfNotEmpty(opArgs, "service", computePortService)

		return runComputeRaw(cmd, "compute.expose_port", opArgs)
	},
}

var computeSandboxesUnexposeCmd = &cobra.Command{
	Use:   "unexpose <id> <port>",
	Short: "Remove an exposed sandbox port",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		port, err := strconv.Atoi(args[1])
		if err != nil || port < 1 {
			return fmt.Errorf("port must be a positive integer, got %q", args[1])
		}
		opArgs := computeMutationArgs(args[0])
		opArgs["port"] = port

		return runComputeRaw(cmd, "compute.unexpose_port", opArgs)
	},
}

// Images.

var computeImagesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List images (named first, then raw snapshots)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runComputeRaw(cmd, "compute.list_images", computeListArgs())
	},
}

var computeImagesGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Get one image by snapshot id or name@version",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runComputeRaw(cmd, "compute.get_image", computeIDArgs(args[0]))
	},
}

var computeImagesDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a raw image (async); named images are deactivated instead",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runComputeRaw(cmd, "compute.delete_image", computeMutationArgs(args[0]))
	},
}

var computeImagesForkCmd = &cobra.Command{
	Use:   "fork <id>",
	Short: "Fan out sandboxes from an image (async)",
	Long: `Fan out --count sandboxes from an image. Raw images fork directly; named warm
images fork their backing snapshot. To reconstitute a single sandbox, use
'panda compute sandboxes create --snapshot <id>' instead.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runComputeRaw(cmd, "compute.fork_image", computeForkArgs(cmd, args[0]))
	},
}

var computeImagesPromoteCmd = &cobra.Command{
	Use:   "promote <id>",
	Short: "Promote a raw image into a named warm image",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		opArgs := computeMutationArgs(args[0])
		opArgs["name"] = computeName
		setIfNotEmpty(opArgs, "version", computeVersion)
		setIfNotEmpty(opArgs, "description", computeDescription)

		if cmd.Flags().Changed("replace") {
			opArgs["replace"] = computeReplace
		}

		if len(computeTags) > 0 {
			opArgs["tags"] = computeTags
		}

		return runComputeRaw(cmd, "compute.promote_image", opArgs)
	},
}

var computeImagesDeactivateCmd = &cobra.Command{
	Use:   "deactivate <id>",
	Short: "Retire a named image (accepts name or name@version)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runComputeRaw(cmd, "compute.deactivate_image", computeMutationArgs(args[0]))
	},
}

var computeImagesLineageCmd = &cobra.Command{
	Use:   "lineage <id>",
	Short: "Show the full lineage tree rooted at an image",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runComputeRaw(cmd, "compute.get_image_lineage", computeIDArgs(args[0]))
	},
}

var computeImagesChildrenCmd = &cobra.Command{
	Use:   "children <id>",
	Short: "List sandboxes created from an image",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runComputeRaw(cmd, "compute.get_image_restored_by", computeIDArgs(args[0]))
	},
}

// Forks.

var computeForksListCmd = &cobra.Command{
	Use:   "list",
	Short: "List fork operations and their progress counts",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runComputeRaw(cmd, "compute.list_forks", computeArgs())
	},
}

var computeForksGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Get one fork operation, including per-child state",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runComputeRaw(cmd, "compute.get_fork", computeIDArgs(args[0]))
	},
}

// Bakes.

var computeBakesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List scheduled image bakes and their status",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runComputeRaw(cmd, "compute.list_bakes", computeListArgs())
	},
}

var computeBakesRunCmd = &cobra.Command{
	Use:   "run <name>",
	Short: "Trigger a bake outside its schedule (async)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		opArgs := computeArgs()
		opArgs["name"] = args[0]
		opArgs["idempotency_key"] = computeIdemOrGenerated()

		return runComputeRaw(cmd, "compute.run_bake", opArgs)
	},
}

// Operations.

var computeOperationsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List async operations",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runComputeRaw(cmd, "compute.list_operations", computeListArgs())
	},
}

var computeOperationsGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Get one async operation by id (poll for completion)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runComputeRaw(cmd, "compute.get_operation", computeIDArgs(args[0]))
	},
}

// SSH keys.

var computeKeysListCmd = &cobra.Command{
	Use:   "list",
	Short: "List your registered SSH public keys",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runComputeRaw(cmd, "compute.list_ssh_keys", computeListArgs())
	},
}

var computeKeysAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Register an SSH public key",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		opArgs := computeArgs()
		opArgs["public_key"] = computePublicKey
		setIfNotEmpty(opArgs, "name", computeName)

		return runComputeRaw(cmd, "compute.add_ssh_key", opArgs)
	},
}

var computeKeysDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a registered SSH public key",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runComputeRaw(cmd, "compute.delete_ssh_key", computeIDArgs(args[0]))
	},
}

// Users.

var computeUsersListCmd = &cobra.Command{
	Use:   "list",
	Short: "List directory users",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runComputeRaw(cmd, "compute.list_users", computeArgs())
	},
}

var computeUsersGetCmd = &cobra.Command{
	Use:   "get <handle>",
	Short: "Get one directory user by handle",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		opArgs := computeArgs()
		opArgs["handle"] = args[0]

		return runComputeRaw(cmd, "compute.get_user", opArgs)
	},
}

// Nodes.

var computeNodesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List compute nodes",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runComputeRaw(cmd, "compute.list_nodes", computeArgs())
	},
}

var computeNodesGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Get one compute node by id",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runComputeRaw(cmd, "compute.get_node", computeIDArgs(args[0]))
	},
}

// Shared helpers.

func computeArgs() map[string]any {
	args := map[string]any{}
	setIfNotEmpty(args, "datasource", computeDatasource)

	return args
}

func computeListArgs() map[string]any {
	args := computeArgs()
	if computeLimit > 0 {
		args["limit"] = computeLimit
	}

	if computeOffset > 0 {
		args["offset"] = computeOffset
	}

	setIfNotEmpty(args, "cursor", computeCursor)

	if len(computeFilters) > 0 {
		args["filter"] = computeFilters
	}

	return args
}

func computeIDArgs(id string) map[string]any {
	args := computeArgs()
	args["id"] = id

	return args
}

func computeMutationArgs(id string) map[string]any {
	args := computeIDArgs(id)
	args["idempotency_key"] = computeIdemOrGenerated()

	return args
}

// computeForkArgs assembles the shared fork mutation arguments. The paused
// flag is forwarded only when set so the server default (inherit from the
// source) applies otherwise.
func computeForkArgs(cmd *cobra.Command, id string) map[string]any {
	args := computeMutationArgs(id)
	args["count"] = computeForkCount
	args["identity_rng"] = computeForkRNG
	args["identity_clock"] = computeForkClock
	setIfNotEmpty(args, "ttl", computeTTL)
	setIfNotEmpty(args, "deadline", computeForkDeadline)
	setIfNotEmpty(args, "flavor", computeForkFlavor)

	if computeForkMinReady > 0 {
		args["min_ready"] = computeForkMinReady
	}

	if cmd.Flags().Changed("paused") {
		args["paused"] = computeForkPaused
	}

	return args
}

// computeIdemOrGenerated returns the user-supplied idempotency key or mints a
// random one: the upstream API requires the header on every mutation, and a
// per-invocation key preserves safe manual retries via --idempotency-key.
func computeIdemOrGenerated() string {
	if computeIdempotency != "" {
		return computeIdempotency
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("panda-%d", time.Now().UnixNano())
	}

	return "panda-" + hex.EncodeToString(buf)
}

// runComputeRaw runs a compute operation and renders the response. Output is
// human-readable by default (a table for list results, key-value pairs for a
// single object) and raw JSON when --output json is set. List results honour
// --filter, which the server applies before pagination.
func runComputeRaw(cmd *cobra.Command, operationID string, args map[string]any) error {
	response, err := runServerOperationRaw(cmd, operationID, args)
	if err != nil {
		return err
	}

	return renderComputeRaw(operationID, response.Body)
}

func keyValueArgsToMap(values []string) (map[string]any, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make(map[string]any, len(values))
	for _, entry := range values {
		key, value, found := strings.Cut(entry, "=")
		if !found || key == "" {
			return nil, fmt.Errorf("%q is not KEY=VALUE", entry)
		}
		out[key] = value
	}

	return out, nil
}
