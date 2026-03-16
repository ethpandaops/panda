package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	authclient "github.com/ethpandaops/panda/pkg/auth/client"
	authstore "github.com/ethpandaops/panda/pkg/auth/store"
	"github.com/ethpandaops/panda/pkg/config"
)

const defaultProxyAuthClientID = "panda"

type authTarget struct {
	mode      string
	issuerURL string
	clientID  string
	resource  string
	proxyURL  string
	enabled   bool
}

var (
	authIssuerURL string
	authClientID  string
	authResource  string
	noBrowser     bool
)

var authCmd = &cobra.Command{
	GroupID: groupSetup,
	Use:     "auth",
	Short:   "Manage proxy authentication",
	Long:    `Authenticate the local server against a hosted credential proxy.`,
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

	authLoginCmd.Flags().BoolVar(&noBrowser, "no-browser", false,
		"manual auth flow for SSH/headless environments (auto-detected over SSH)")

	for _, cmd := range []*cobra.Command{authLoginCmd, authLogoutCmd, authStatusCmd} {
		cmd.Flags().StringVar(&authIssuerURL, "issuer", "", "proxy auth issuer URL (defaults to the configured server's proxy auth issuer)")
		cmd.Flags().StringVar(&authClientID, "client-id", "", "OAuth client ID (defaults to configured value or 'panda')")
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

	headless := isHeadlessAuth()
	if headless && !noBrowser {
		fmt.Println("SSH session detected, using device authorization flow.")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	clientCfg := authclient.Config{
		IssuerURL: target.issuerURL,
		ClientID:  target.clientID,
		Resource:  target.resource,
		Headless:  headless,
	}

	if target.proxyURL != "" {
		clientCfg.BrandingURL = strings.TrimRight(target.proxyURL, "/") + "/auth/branding"
	}

	client := authclient.New(log, clientCfg)

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

	// Restart the server if it's running so it picks up the new credentials.
	restartServerIfRunning()

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
	// 1. Explicit CLI flags take priority.
	if strings.TrimSpace(authIssuerURL) != "" || strings.TrimSpace(authClientID) != "" || strings.TrimSpace(authResource) != "" {
		target := &authTarget{
			mode:      "oidc",
			issuerURL: strings.TrimSpace(authIssuerURL),
			clientID:  strings.TrimSpace(authClientID),
			resource:  strings.TrimSpace(authResource),
			enabled:   true,
		}

		if target.clientID == "" {
			target.clientID = defaultProxyAuthClientID
		}

		if target.resource == "" && target.mode != "oidc" {
			target.resource = target.issuerURL
		}

		if target.issuerURL == "" {
			return nil, fmt.Errorf("issuer is required when overriding auth settings")
		}

		return target, nil
	}

	// 2. Try reading proxy.auth from local config file (works without a running server).
	if target := resolveAuthTargetFromConfig(); target != nil {
		return target, nil
	}

	// 3. Fall back to querying the running server's proxy auth metadata endpoint.
	metadata, err := proxyAuthMetadata(ctx)
	if err != nil {
		return nil, fmt.Errorf(
			"could not resolve proxy auth settings: no proxy.auth in config and server unreachable (%w). "+
				"Run 'panda init' to create a config with proxy auth settings, or start the server first",
			err,
		)
	}

	target := &authTarget{
		mode:      strings.TrimSpace(metadata.Mode),
		issuerURL: strings.TrimSpace(metadata.IssuerURL),
		clientID:  strings.TrimSpace(metadata.ClientID),
		resource:  strings.TrimSpace(metadata.Resource),
		enabled:   metadata.Enabled,
	}

	if target.mode == "" {
		target.mode = "oauth"
	}

	if target.clientID == "" {
		target.clientID = defaultProxyAuthClientID
	}

	if target.resource == "" && target.mode != "oidc" {
		target.resource = target.issuerURL
	}

	return target, nil
}

// resolveAuthTargetFromConfig attempts to read proxy auth settings directly
// from the local config file, enabling auth to work without a running server.
func resolveAuthTargetFromConfig() *authTarget {
	cfg, err := config.LoadClient(cfgFile)
	if err != nil {
		return nil
	}

	if cfg.Proxy.Auth == nil {
		return nil
	}

	issuerURL := strings.TrimSpace(cfg.Proxy.Auth.IssuerURL)
	if issuerURL == "" {
		// Fall back to proxy URL as issuer if issuer_url is not explicitly set.
		issuerURL = strings.TrimRight(strings.TrimSpace(cfg.Proxy.URL), "/")
	}

	if issuerURL == "" {
		return nil
	}

	clientID := strings.TrimSpace(cfg.Proxy.Auth.ClientID)
	if clientID == "" {
		clientID = defaultProxyAuthClientID
	}

	mode := strings.TrimSpace(cfg.Proxy.Auth.Mode)
	if mode == "" {
		mode = "oauth"
	}

	resource := strings.TrimSpace(cfg.Proxy.Auth.Resource)
	if resource == "" && mode != "oidc" {
		resource = issuerURL
		if resource == "" {
			resource = strings.TrimRight(strings.TrimSpace(cfg.Proxy.URL), "/")
		}
	}

	return &authTarget{
		mode:      mode,
		issuerURL: issuerURL,
		clientID:  clientID,
		resource:  resource,
		proxyURL:  strings.TrimRight(strings.TrimSpace(cfg.Proxy.URL), "/"),
		enabled:   true,
	}
}

// isHeadlessAuth returns true when the auth flow should skip the local
// callback server — either because --no-browser was passed or because
// an SSH session was detected.
func isHeadlessAuth() bool {
	return noBrowser || isSSHSession()
}

// isSSHSession returns true when the process is running inside an SSH session.
func isSSHSession() bool {
	for _, key := range []string{"SSH_CLIENT", "SSH_CONNECTION", "SSH_TTY"} {
		if os.Getenv(key) != "" {
			return true
		}
	}

	return false
}
