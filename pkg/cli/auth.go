package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	authclient "github.com/ethpandaops/mcp/pkg/auth/client"
	authstore "github.com/ethpandaops/mcp/pkg/auth/store"
)

const defaultProxyAuthClientID = "ep"

type authTarget struct {
	issuerURL string
	clientID  string
	resource  string
	enabled   bool
}

var (
	authIssuerURL string
	authClientID  string
	authResource  string
)

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage proxy authentication",
	Long:  `Authenticate the local server against a hosted credential proxy.`,
}

var authLoginCmd = &cobra.Command{
	Use:   "login",
	Short: "Log in to the configured credential proxy",
	RunE:  runAuthLogin,
}

var authLogoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Remove locally stored proxy credentials",
	RunE:  runAuthLogout,
}

var authStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show proxy authentication status",
	RunE:  runAuthStatus,
}

func init() {
	rootCmd.AddCommand(authCmd)
	authCmd.AddCommand(authLoginCmd)
	authCmd.AddCommand(authLogoutCmd)
	authCmd.AddCommand(authStatusCmd)

	for _, cmd := range []*cobra.Command{authLoginCmd, authLogoutCmd, authStatusCmd} {
		cmd.Flags().StringVar(&authIssuerURL, "issuer", "", "proxy auth issuer URL (defaults to the configured server's proxy auth issuer)")
		cmd.Flags().StringVar(&authClientID, "client-id", "", "OAuth client ID (defaults to configured value or 'ep')")
		cmd.Flags().StringVar(&authResource, "resource", "", "OAuth protected resource (defaults to the proxy URL)")
	}
}

func runAuthLogin(_ *cobra.Command, _ []string) error {
	target, err := resolveAuthTarget(context.Background())
	if err != nil {
		return err
	}

	if !target.enabled {
		fmt.Println("Proxy authentication is not enabled for the configured server.")
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	client := authclient.New(log, authclient.Config{
		IssuerURL: target.issuerURL,
		ClientID:  target.clientID,
		Resource:  target.resource,
	})

	tokens, err := client.Login(ctx)
	if err != nil {
		return fmt.Errorf("login failed: %w", err)
	}

	store := authstore.New(log, authstore.Config{
		AuthClient: client,
		IssuerURL:  target.issuerURL,
		ClientID:   target.clientID,
		Resource:   target.resource,
	})

	if err := store.Save(tokens); err != nil {
		return fmt.Errorf("saving tokens: %w", err)
	}

	fmt.Printf("Authenticated to %s\n", target.issuerURL)
	fmt.Printf("Credentials stored at: %s\n", store.Path())
	fmt.Printf("Token expires at: %s\n", tokens.ExpiresAt.Format(time.RFC3339))

	return nil
}

func runAuthLogout(_ *cobra.Command, _ []string) error {
	target, err := resolveAuthTarget(context.Background())
	if err != nil {
		return err
	}

	store := authstore.New(log, authstore.Config{
		IssuerURL: target.issuerURL,
		ClientID:  target.clientID,
		Resource:  target.resource,
	})

	if err := store.Clear(); err != nil {
		return fmt.Errorf("clearing tokens: %w", err)
	}

	fmt.Printf("Removed credentials at: %s\n", store.Path())
	return nil
}

func runAuthStatus(_ *cobra.Command, _ []string) error {
	target, err := resolveAuthTarget(context.Background())
	if err != nil {
		return err
	}

	if !target.enabled {
		fmt.Println("Proxy authentication is not enabled for the configured server.")
		return nil
	}

	client := authclient.New(log, authclient.Config{
		IssuerURL: target.issuerURL,
		ClientID:  target.clientID,
		Resource:  target.resource,
	})

	store := authstore.New(log, authstore.Config{
		AuthClient: client,
		IssuerURL:  target.issuerURL,
		ClientID:   target.clientID,
		Resource:   target.resource,
	})

	tokens, err := store.Load()
	if err != nil {
		return fmt.Errorf("loading tokens: %w", err)
	}

	fmt.Printf("Issuer: %s\n", target.issuerURL)
	fmt.Printf("Client ID: %s\n", target.clientID)
	fmt.Printf("Resource: %s\n", target.resource)
	fmt.Printf("Credentials: %s\n", store.Path())

	if tokens == nil {
		fmt.Println("Status: Not authenticated")
		return nil
	}

	if tokens.ExpiresAt.After(time.Now()) {
		fmt.Printf("Status: Authenticated (expires in %s)\n", time.Until(tokens.ExpiresAt).Round(time.Second))
		fmt.Printf("Expires at: %s\n", tokens.ExpiresAt.Format(time.RFC3339))
		return nil
	}

	fmt.Printf("Status: Expired (expired at %s)\n", tokens.ExpiresAt.Format(time.RFC3339))
	return nil
}

func resolveAuthTarget(ctx context.Context) (*authTarget, error) {
	if strings.TrimSpace(authIssuerURL) != "" || strings.TrimSpace(authClientID) != "" || strings.TrimSpace(authResource) != "" {
		target := &authTarget{
			issuerURL: strings.TrimSpace(authIssuerURL),
			clientID:  strings.TrimSpace(authClientID),
			resource:  strings.TrimSpace(authResource),
			enabled:   true,
		}

		if target.clientID == "" {
			target.clientID = defaultProxyAuthClientID
		}

		if target.resource == "" {
			target.resource = target.issuerURL
		}

		if target.issuerURL == "" {
			return nil, fmt.Errorf("issuer is required when overriding auth settings")
		}

		return target, nil
	}

	metadata, err := proxyAuthMetadata(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading proxy auth metadata from server: %w", err)
	}

	target := &authTarget{
		issuerURL: strings.TrimSpace(metadata.IssuerURL),
		clientID:  strings.TrimSpace(metadata.ClientID),
		resource:  strings.TrimSpace(metadata.Resource),
		enabled:   metadata.Enabled,
	}

	if target.clientID == "" {
		target.clientID = defaultProxyAuthClientID
	}

	if target.resource == "" {
		target.resource = target.issuerURL
	}

	return target, nil
}
