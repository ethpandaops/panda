package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/ethpandaops/panda/pkg/serverapi"
)

// defaultProxyAuthClientID is the OAuth client ID assumed when a config does not
// specify one.
const defaultProxyAuthClientID = "panda"

// minLoginPollInterval bounds how often the CLI polls the server for login
// completion, regardless of the provider's advertised interval.
const minLoginPollInterval = 2 * time.Second

var authCmd = &cobra.Command{
	GroupID: groupSetup,
	Use:     "auth",
	Short:   "Manage proxy authentication",
	Long: `Manage the server's proxy authentication.

The server owns the proxy credentials: it performs the login, holds the tokens,
and refreshes them. These commands drive the running server over its API and
never read or write the credential files themselves, so a running server is
required.`,
}

var authLoginCmd = &cobra.Command{
	Use:   "login",
	Short: "Log the server in to the configured credential proxy",
	RunE:  runAuthLogin,
}

var authLogoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Remove the server's stored proxy credentials",
	RunE:  runAuthLogout,
}

var authStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the server's proxy authentication status",
	RunE:  runAuthStatus,
}

func init() {
	rootCmd.AddCommand(authCmd)
	authCmd.AddCommand(authLoginCmd)
	authCmd.AddCommand(authLogoutCmd)
	authCmd.AddCommand(authStatusCmd)
}

// runAuthLogin asks the server to start a device-authorization login, prints the
// verification details for the user, and waits for the server to complete the
// flow. The tokens are minted into and held by the server; they never reach the
// CLI.
func runAuthLogin(cmd *cobra.Command, _ []string) error {
	ctx := commandContext(cmd)

	var resp serverapi.AuthLoginResponse
	if err := serverPostJSON(ctx, "/api/v1/auth/login", nil, &resp); err != nil {
		return fmt.Errorf("starting login: %w", err)
	}

	if !resp.Enabled {
		fmt.Println("Proxy authentication is not enabled for the configured server.")

		return nil
	}

	fmt.Printf("\nTo authenticate, open:\n\n  %s\n\n", resp.VerificationURI)
	fmt.Printf("and enter the code:\n\n  %s\n\n", resp.UserCode)

	if resp.VerificationURIComplete != "" {
		fmt.Printf("(or open %s to skip entering the code)\n\n", resp.VerificationURIComplete)
	}

	fmt.Println("Waiting for authorization...")

	// Bound the wait by the device code's lifetime; the server also stops
	// polling when the code expires.
	if resp.ExpiresIn > 0 {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, time.Duration(resp.ExpiresIn+30)*time.Second)
		defer cancel()
	}

	interval := time.Duration(resp.Interval) * time.Second
	if interval < minLoginPollInterval {
		interval = minLoginPollInterval
	}

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for authorization")
		case <-time.After(interval):
		}

		var state serverapi.AuthLoginStateResponse
		if err := serverGetJSON(ctx, "/api/v1/auth/login", nil, &state); err != nil {
			return fmt.Errorf("checking login status: %w", err)
		}

		switch state.State {
		case serverapi.AuthLoginAuthenticated:
			fmt.Println("Authenticated.")

			return nil
		case serverapi.AuthLoginError:
			return fmt.Errorf("login failed: %s", state.Error)
		case serverapi.AuthLoginNone:
			return fmt.Errorf("login is no longer in progress")
		case serverapi.AuthLoginPending:
		}
	}
}

// runAuthLogout asks the server to clear its stored credentials.
func runAuthLogout(cmd *cobra.Command, _ []string) error {
	if err := serverPostJSON(commandContext(cmd), "/api/v1/auth/logout", nil, nil); err != nil {
		return fmt.Errorf("logging out: %w", err)
	}

	fmt.Println("Removed the server's stored credentials.")

	return nil
}

// runAuthStatus reports the server's credential state.
func runAuthStatus(cmd *cobra.Command, _ []string) error {
	var st serverapi.AuthStatusResponse
	if err := serverGetJSON(commandContext(cmd), "/api/v1/auth/status", nil, &st); err != nil {
		return fmt.Errorf("getting auth status: %w", err)
	}

	if !st.Enabled {
		fmt.Println("Proxy authentication is not enabled for the configured server.")

		return nil
	}

	fmt.Printf("Issuer: %s\n", st.IssuerURL)
	fmt.Printf("Client ID: %s\n", st.ClientID)
	fmt.Printf("Resource: %s\n", st.Resource)
	fmt.Printf("Credentials: %s\n", st.CredentialsPath)

	switch {
	case !st.Authenticated:
		fmt.Println("Status: Not authenticated")

		return nil
	case st.ExpiresAt == nil:
		fmt.Println("Status: Authenticated")
	case st.Expired:
		fmt.Printf("Status: Expired (expired at %s)\n", st.ExpiresAt.Format(time.RFC3339))
	default:
		fmt.Printf("Status: Authenticated (expires in %s)\n", time.Until(*st.ExpiresAt).Round(time.Second))
		fmt.Printf("Expires at: %s\n", st.ExpiresAt.Format(time.RFC3339))
	}

	printRefreshTokenStatus(&st)

	return nil
}

// printRefreshTokenStatus reports what is knowable about the refresh token. The
// refresh token itself is never exposed; only its presence and last rotation
// time are reported by the server.
func printRefreshTokenStatus(st *serverapi.AuthStatusResponse) {
	switch {
	case !st.RefreshTokenPresent:
		fmt.Println("Refresh token: none (session cannot auto-refresh; it ends when the access token expires)")
	case st.RefreshTokenIssuedAt == nil:
		fmt.Println("Refresh token: present (last rotation time unknown; the provider reused it on the last refresh)")
	default:
		fmt.Printf("Refresh token: present (last rotated %s ago, at %s)\n",
			time.Since(*st.RefreshTokenIssuedAt).Round(time.Second),
			st.RefreshTokenIssuedAt.Format(time.RFC3339),
		)
	}
}
