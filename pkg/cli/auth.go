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
	"github.com/ethpandaops/panda/pkg/serverapi"
)

const defaultProxyAuthClientID = "panda"

type authTarget struct {
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

// newAuthClient builds an auth client for the resolved target, wiring the
// proxy branding URL when a proxy URL is known.
func newAuthClient(target *authTarget, headless bool) authclient.Client {
	cfg := authclient.Config{
		IssuerURL: target.issuerURL,
		ClientID:  target.clientID,
		Resource:  target.resource,
		Headless:  headless,
	}

	if target.proxyURL != "" {
		cfg.BrandingURL = strings.TrimRight(target.proxyURL, "/") + "/auth/branding"
	}

	return authclient.New(log, cfg)
}

// newAuthStore builds the credential store for the resolved target. The store
// uses client for token refresh when one is supplied.
func newAuthStore(target *authTarget, client authclient.Client) authstore.Store {
	return authstore.New(log, authstore.Config{
		AuthClient: client,
		IssuerURL:  target.issuerURL,
		ClientID:   target.clientID,
		Resource:   target.resource,
	})
}

func runAuthLogin(cmd *cobra.Command, _ []string) error {
	baseCtx := commandContext(cmd)

	target, err := resolveAuthTarget(baseCtx)
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

	ctx, cancel := context.WithTimeout(baseCtx, 5*time.Minute)
	defer cancel()

	client := newAuthClient(target, headless)

	tokens, err := client.Login(ctx)
	if err != nil {
		return fmt.Errorf("login failed: %w", err)
	}

	store := newAuthStore(target, client)

	if err := store.Save(tokens); err != nil {
		return fmt.Errorf("saving tokens: %w", err)
	}

	fmt.Printf("Authenticated to %s\n", target.issuerURL)
	fmt.Printf("Credentials stored at: %s\n", store.Path())
	fmt.Printf("Token expires at: %s\n", tokens.ExpiresAt.Format(time.RFC3339))

	// Restart the server if it's running so it picks up the new credentials.
	restartServerIfRunning(baseCtx)

	return nil
}

func runAuthLogout(cmd *cobra.Command, _ []string) error {
	target, err := resolveAuthTarget(cmd.Context())
	if err != nil {
		return err
	}

	store := newAuthStore(target, nil)

	if err := store.Clear(); err != nil {
		return fmt.Errorf("clearing tokens: %w", err)
	}

	fmt.Printf("Removed credentials at: %s\n", store.Path())
	return nil
}

func runAuthStatus(cmd *cobra.Command, _ []string) error {
	target, err := resolveAuthTarget(cmd.Context())
	if err != nil {
		return err
	}

	if !target.enabled {
		fmt.Println("Proxy authentication is not enabled for the configured server.")
		return nil
	}

	client := newAuthClient(target, false)
	store := newAuthStore(target, client)

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
			issuerURL: strings.TrimSpace(authIssuerURL),
			clientID:  strings.TrimSpace(authClientID),
			resource:  strings.TrimSpace(authResource),
			enabled:   true,
		}

		if target.clientID == "" {
			target.clientID = defaultProxyAuthClientID
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

	mode := strings.TrimSpace(metadata.Mode)
	if mode == "" {
		mode = "oauth"
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

	if target.resource == "" && mode != "oidc" {
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

func proxyAuthMetadata(ctx context.Context) (*serverapi.ProxyAuthMetadataResponse, error) {
	var response serverapi.ProxyAuthMetadataResponse
	if err := serverGetJSON(ctx, "/api/v1/proxy/auth", nil, &response); err != nil {
		return nil, err
	}

	return &response, nil
}
