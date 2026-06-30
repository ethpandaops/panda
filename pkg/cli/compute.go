package cli

import (
	"context"
	"time"

	"github.com/spf13/cobra"
)

// computeProbeTimeout bounds the help-time datasource probe that decides
// whether the compute command group is advertised.
const computeProbeTimeout = 2 * time.Second

var (
	computeDatasource  string
	computeTTL         string
	computeOnDelete    string
	computeNote        string
	computeExtend      string
	computeTemplate    string
	computeName        string
	computePublicKey   string
	computeIdempotency string
	computeLimit       int
	computeOffset      int
	computeCursor      string
)

var computeCmd = &cobra.Command{
	GroupID: groupDirect,
	Use:     "compute <resource> <command>",
	Short:   "Manage ephemeral compute sandboxes: create, snapshot, restore, lease",
	Long: `Manage compute, the ethpandaops ephemeral-sandbox control plane. A sandbox is
a short-lived microVM created from a template; you can snapshot it, stop and
start it, extend its lease (TTL), restore snapshots into new sandboxes, and
poll the async operations that back every mutation.

Commands are grouped by resource. Most mutations are asynchronous: they return
an operation id you can poll with 'panda compute operations get <id>'.

Access is restricted to core ethpandaops members. The command is only shown
when a compute datasource is reachable through the configured proxies.

The --datasource flag can be omitted when a single compute datasource is
configured.

Examples:
  panda compute datasources
  panda compute templates list
  panda compute sandboxes create --template ubuntu/24.04 --ttl 1h
  panda compute sandboxes list
  panda compute sandboxes get <id>
  panda compute sandboxes snapshot <id> --note "before upgrade"
  panda compute sandboxes lease <id> --extend 30m
  panda compute snapshots restore <snapshot_id> --ttl 2h
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
		computeSandboxesListCmd, computeSnapshotsListCmd, computeTemplatesListCmd,
		computeOperationsListCmd, computeKeysListCmd,
	} {
		cmd.Flags().IntVar(&computeLimit, "limit", 0, "Maximum items to return")
		cmd.Flags().IntVar(&computeOffset, "offset", 0, "Items to skip")
		cmd.Flags().StringVar(&computeCursor, "cursor", "", "Pagination cursor (next_cursor from a prior page)")
	}

	computeSandboxesCreateCmd.Flags().StringVar(&computeTemplate, "template", "", "Template to launch (required)")
	computeSandboxesCreateCmd.Flags().StringVar(&computeTTL, "ttl", "", "Lease duration (Go duration, e.g. 1h)")
	computeSandboxesCreateCmd.Flags().StringVar(&computeOnDelete, "on-delete", "",
		"Disposition on delete: archive, cold, delete, or hot")
	_ = computeSandboxesCreateCmd.MarkFlagRequired("template")

	computeSandboxesSnapshotCmd.Flags().StringVar(&computeNote, "note", "", "Optional note recorded with the snapshot")
	computeSandboxesSnapshotCmd.Flags().StringVar(&computeTTL, "ttl", "",
		"Snapshot lifetime (Go duration; \"0\" means no expiry, omit for the server default)")
	computeSandboxesLeaseCmd.Flags().StringVar(&computeExtend, "extend", "",
		"Lease extension (Go duration, e.g. 30m) (required)")
	_ = computeSandboxesLeaseCmd.MarkFlagRequired("extend")
	computeSnapshotsRestoreCmd.Flags().StringVar(&computeTTL, "ttl", "",
		"Lease duration for the restored sandbox (Go duration)")

	computeKeysAddCmd.Flags().StringVar(&computePublicKey, "public-key", "", "SSH public key material (required)")
	computeKeysAddCmd.Flags().StringVar(&computeName, "name", "", "Optional label for the key")
	_ = computeKeysAddCmd.MarkFlagRequired("public-key")

	for _, cmd := range []*cobra.Command{
		computeSandboxesCreateCmd, computeSandboxesDeleteCmd, computeSandboxesStopCmd,
		computeSandboxesStartCmd, computeSandboxesSnapshotCmd, computeSnapshotsDeleteCmd,
		computeSnapshotsRestoreCmd,
	} {
		cmd.Flags().StringVar(&computeIdempotency, "idempotency-key", "",
			"Idempotency key to make the mutation safely retryable")
	}

	computeSandboxesCmd.AddCommand(
		computeSandboxesListCmd, computeSandboxesGetCmd, computeSandboxesCreateCmd,
		computeSandboxesDeleteCmd, computeSandboxesStopCmd, computeSandboxesStartCmd,
		computeSandboxesSnapshotCmd, computeSandboxesLeaseCmd, computeSandboxesSnapshotsCmd,
		computeSandboxesOperationsCmd, computeSandboxesLogsCmd, computeSandboxesLineageCmd,
	)
	computeSnapshotsCmd.AddCommand(
		computeSnapshotsListCmd, computeSnapshotsGetCmd, computeSnapshotsDeleteCmd,
		computeSnapshotsRestoreCmd, computeSnapshotsChildrenCmd,
	)
	computeTemplatesCmd.AddCommand(computeTemplatesListCmd, computeTemplatesGetCmd)
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
		computeSnapshotsCmd,
		computeTemplatesCmd,
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

var computeSnapshotsCmd = &cobra.Command{
	Use:   "snapshots <command>",
	Short: "List, restore, and delete snapshots",
}

var computeTemplatesCmd = &cobra.Command{
	Use:   "templates <command>",
	Short: "List available sandbox templates",
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
	Short: "Create a sandbox from a template (async)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		opArgs := computeArgs()
		opArgs["template"] = computeTemplate
		setIfNotEmpty(opArgs, "ttl", computeTTL)
		setIfNotEmpty(opArgs, "on_delete", computeOnDelete)
		setIfNotEmpty(opArgs, "idempotency_key", computeIdempotency)

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

var computeSandboxesSnapshotsCmd = &cobra.Command{
	Use:   "snapshots <id>",
	Short: "List snapshots taken from a sandbox",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runComputeRaw(cmd, "compute.get_sandbox_snapshots", computeIDArgs(args[0]))
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
		return runComputeRaw(cmd, "compute.get_sandbox_logs", computeIDArgs(args[0]))
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

// Snapshots.

var computeSnapshotsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List snapshots",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runComputeRaw(cmd, "compute.list_snapshots", computeListArgs())
	},
}

var computeSnapshotsGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Get one snapshot by id",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runComputeRaw(cmd, "compute.get_snapshot", computeIDArgs(args[0]))
	},
}

var computeSnapshotsDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a snapshot (async)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runComputeRaw(cmd, "compute.delete_snapshot", computeMutationArgs(args[0]))
	},
}

var computeSnapshotsRestoreCmd = &cobra.Command{
	Use:   "restore <id>",
	Short: "Restore a snapshot into a new sandbox (async)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		opArgs := computeMutationArgs(args[0])
		setIfNotEmpty(opArgs, "ttl", computeTTL)

		return runComputeRaw(cmd, "compute.restore_snapshot", opArgs)
	},
}

var computeSnapshotsChildrenCmd = &cobra.Command{
	Use:   "children <id>",
	Short: "List sandboxes restored from a snapshot",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runComputeRaw(cmd, "compute.get_snapshot_restored_by", computeIDArgs(args[0]))
	},
}

// Templates.

var computeTemplatesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available sandbox templates",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runComputeRaw(cmd, "compute.list_templates", computeListArgs())
	},
}

var computeTemplatesGetCmd = &cobra.Command{
	Use:   "get <name> <version>",
	Short: "Get one template by name and version",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		opArgs := computeArgs()
		opArgs["name"] = args[0]
		opArgs["version"] = args[1]

		return runComputeRaw(cmd, "compute.get_template", opArgs)
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

	return args
}

func computeIDArgs(id string) map[string]any {
	args := computeArgs()
	args["id"] = id

	return args
}

func computeMutationArgs(id string) map[string]any {
	args := computeIDArgs(id)
	setIfNotEmpty(args, "idempotency_key", computeIdempotency)

	return args
}

// runComputeRaw runs a compute operation and prints the raw JSON response.
func runComputeRaw(cmd *cobra.Command, operationID string, args map[string]any) error {
	response, err := runServerOperationRaw(cmd, operationID, args)
	if err != nil {
		return err
	}

	return printJSONBytes(response.Body)
}
