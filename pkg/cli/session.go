package cli

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/spf13/cobra"

	"github.com/ethpandaops/panda/pkg/serverapi"
)

var sessionCmd = &cobra.Command{
	GroupID: groupWorkflow,
	Use:     "session",
	Short:   "Manage sandbox sessions",
	Long: `Manage persistent sandbox sessions. Sessions keep containers alive
between executions, preserving files in /workspace.

Examples:
  panda session list
  panda session create
  panda session destroy <session-id>`,
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

	sessionDestroyCmd.ValidArgsFunction = completeSessionIDs
}

func runSessionList(cmd *cobra.Command, _ []string) error {
	response, err := listSessions(cmd.Context())
	if err != nil {
		return fmt.Errorf("listing sessions: %w", err)
	}

	if isJSON() {
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

func runSessionCreate(cmd *cobra.Command, _ []string) error {
	response, err := createSession(cmd.Context())
	if err != nil {
		return fmt.Errorf("creating session: %w", err)
	}

	if isJSON() {
		return printJSON(response)
	}

	fmt.Println(response.SessionID)

	return nil
}

func runSessionDestroy(cmd *cobra.Command, args []string) error {
	if err := destroySession(cmd.Context(), args[0]); err != nil {
		return fmt.Errorf("destroying session: %w", err)
	}

	if !isJSON() {
		fmt.Printf("Session %s destroyed.\n", args[0])
	}

	return nil
}

func listSessions(ctx context.Context) (*serverapi.ListSessionsResponse, error) {
	var response serverapi.ListSessionsResponse
	if err := serverGetJSON(ctx, "/api/v1/sessions", nil, &response); err != nil {
		return nil, err
	}

	return &response, nil
}

func createSession(ctx context.Context) (*serverapi.CreateSessionResponse, error) {
	var response serverapi.CreateSessionResponse
	if err := serverPostJSON(ctx, "/api/v1/sessions", map[string]any{}, &response); err != nil {
		return nil, err
	}

	return &response, nil
}

func destroySession(ctx context.Context, sessionID string) error {
	return serverDelete(ctx, "/api/v1/sessions/"+url.PathEscape(sessionID))
}
