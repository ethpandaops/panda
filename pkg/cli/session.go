package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
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

func runSessionList(_ *cobra.Command, _ []string) error {
	ctx := context.Background()
	response, err := listSessions(ctx)
	if err != nil {
		return fmt.Errorf("listing sessions: %w", err)
	}

	if sessionJSON {
		return printJSON(response)
	}

	if len(response.Sessions) == 0 {
		fmt.Println("No active sessions.")

		return nil
	}

	for _, s := range response.Sessions {
		fmt.Printf("  %-36s  created=%s  ttl=%s  files=%d\n",
			s.SessionID,
			s.CreatedAt.Format(time.RFC3339),
			s.TTLRemaining,
			len(s.WorkspaceFiles),
		)
	}

	return nil
}

func runSessionCreate(_ *cobra.Command, _ []string) error {
	ctx := context.Background()
	response, err := createSession(ctx)
	if err != nil {
		return fmt.Errorf("creating session: %w", err)
	}

	if sessionJSON {
		return printJSON(response)
	}

	fmt.Println(response.SessionID)

	return nil
}

func runSessionDestroy(_ *cobra.Command, args []string) error {
	ctx := context.Background()
	if err := destroySession(ctx, args[0]); err != nil {
		return fmt.Errorf("destroying session: %w", err)
	}

	if !sessionJSON {
		fmt.Printf("Session %s destroyed.\n", args[0])
	}

	return nil
}
