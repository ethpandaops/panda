package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/ethpandaops/mcp/pkg/app"
	"github.com/ethpandaops/mcp/pkg/config"
)

var sessionJSON bool

var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Manage sandbox sessions",
	Long: `Manage persistent sandbox sessions. Sessions keep containers alive
between executions, preserving files in /workspace.

Examples:
  ep session list
  ep session create
  ep session destroy <session-id>`,
}

var sessionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List active sessions",
	RunE:  runSessionList,
}

var sessionCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new empty session",
	RunE:  runSessionCreate,
}

var sessionDestroyCmd = &cobra.Command{
	Use:   "destroy <session-id>",
	Short: "Destroy a session",
	Args:  cobra.ExactArgs(1),
	RunE:  runSessionDestroy,
}

func init() {
	rootCmd.AddCommand(sessionCmd)
	sessionCmd.AddCommand(sessionListCmd)
	sessionCmd.AddCommand(sessionCreateCmd)
	sessionCmd.AddCommand(sessionDestroyCmd)

	sessionCmd.PersistentFlags().BoolVar(&sessionJSON, "json", false, "Output in JSON format")
}

func buildSandboxApp(ctx context.Context) (*app.App, *config.Config, error) {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return nil, nil, fmt.Errorf("loading config: %w", err)
	}

	a := app.New(log, cfg)
	if err := a.BuildWithSandbox(ctx); err != nil {
		return nil, nil, fmt.Errorf("building app: %w", err)
	}

	return a, cfg, nil
}

func runSessionList(_ *cobra.Command, _ []string) error {
	ctx := context.Background()

	a, _, err := buildSandboxApp(ctx)
	if err != nil {
		return err
	}

	defer func() { _ = a.Stop(ctx) }()

	if !a.Sandbox.SessionsEnabled() {
		return fmt.Errorf("sessions are not enabled")
	}

	sessions, err := a.Sandbox.ListSessions(ctx, "")
	if err != nil {
		return fmt.Errorf("listing sessions: %w", err)
	}

	if sessionJSON {
		return printJSON(map[string]any{"sessions": sessions})
	}

	if len(sessions) == 0 {
		fmt.Println("No active sessions.")

		return nil
	}

	for _, s := range sessions {
		fmt.Printf("  %-36s  created=%s  ttl=%s  files=%d\n",
			s.ID,
			s.CreatedAt.Format(time.RFC3339),
			s.TTLRemaining.Round(time.Second),
			len(s.WorkspaceFiles),
		)
	}

	return nil
}

func runSessionCreate(_ *cobra.Command, _ []string) error {
	ctx := context.Background()

	a, _, err := buildSandboxApp(ctx)
	if err != nil {
		return err
	}

	defer func() { _ = a.Stop(ctx) }()

	if !a.Sandbox.SessionsEnabled() {
		return fmt.Errorf("sessions are not enabled")
	}

	// Build env for the session container.
	env, envErr := a.SandboxEnv()
	if envErr != nil {
		return fmt.Errorf("building sandbox env: %w", envErr)
	}

	sessionID, err := a.Sandbox.CreateSession(ctx, "", env)
	if err != nil {
		return fmt.Errorf("creating session: %w", err)
	}

	if sessionJSON {
		return printJSON(map[string]string{"session_id": sessionID})
	}

	fmt.Println(sessionID)

	return nil
}

func runSessionDestroy(_ *cobra.Command, args []string) error {
	ctx := context.Background()

	a, _, err := buildSandboxApp(ctx)
	if err != nil {
		return err
	}

	defer func() { _ = a.Stop(ctx) }()

	if err := a.Sandbox.DestroySession(ctx, args[0], ""); err != nil {
		return fmt.Errorf("destroying session: %w", err)
	}

	if !sessionJSON {
		fmt.Printf("Session %s destroyed.\n", args[0])
	}

	return nil
}
