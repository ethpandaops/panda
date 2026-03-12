package cli

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	authclient "github.com/ethpandaops/panda/pkg/auth/client"
	authstore "github.com/ethpandaops/panda/pkg/auth/store"
	"github.com/ethpandaops/panda/pkg/config"
	"github.com/ethpandaops/panda/pkg/configpath"
)

var composeFile string

var serverCmd = &cobra.Command{
	GroupID: groupSetup,
	Use:     "server",
	Short:   "Manage the local panda server",
	Long: `Manage the local panda server lifecycle via Docker Compose.

Examples:
  panda server start
  panda server stop
  panda server status
  panda server logs`,
}

var serverStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the server containers",
	RunE:  runServerStart,
}

var serverStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the server containers",
	RunE:  runServerStop,
}

var serverRestartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart the server containers",
	RunE:  runServerRestart,
}

var serverStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show server container and health status",
	RunE:  runServerStatus,
}

var serverLogsCmd = &cobra.Command{
	Use:   "logs",
	Short: "Stream server container logs",
	RunE:  runServerLogs,
}

var serverUpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "Pull latest images and restart",
	RunE:  runServerUpdate,
}

func init() {
	rootCmd.AddCommand(serverCmd)
	serverCmd.AddCommand(serverStartCmd)
	serverCmd.AddCommand(serverStopCmd)
	serverCmd.AddCommand(serverRestartCmd)
	serverCmd.AddCommand(serverStatusCmd)
	serverCmd.AddCommand(serverLogsCmd)
	serverCmd.AddCommand(serverUpdateCmd)

	serverCmd.PersistentFlags().StringVar(
		&composeFile,
		"compose-file",
		"",
		"path to docker-compose.yaml (default: ~/.config/panda/docker-compose.yaml)",
	)
}

func runServerStart(_ *cobra.Command, _ []string) error {
	return runDockerCompose(resolveComposeFile(), "up", "-d")
}

func runServerStop(_ *cobra.Command, _ []string) error {
	return runDockerCompose(resolveComposeFile(), "down")
}

func runServerRestart(_ *cobra.Command, _ []string) error {
	return runDockerCompose(resolveComposeFile(), "restart")
}

func runServerStatus(_ *cobra.Command, _ []string) error {
	// Show container status.
	if err := runDockerCompose(resolveComposeFile(), "ps"); err != nil {
		return err
	}

	fmt.Println()

	// Show server health.
	printHealthStatus()

	// Show auth status.
	printAuthStatus()

	// Show proxy URL from config.
	printProxyURL()

	return nil
}

func runServerLogs(_ *cobra.Command, _ []string) error {
	return runDockerCompose(resolveComposeFile(), "logs", "-f")
}

func runServerUpdate(_ *cobra.Command, _ []string) error {
	return upgradeServer()
}

// resolveComposeFile returns the docker-compose file path from
// the --compose-file flag or the default config directory.
func resolveComposeFile() string {
	if composeFile != "" {
		return composeFile
	}

	return filepath.Join(
		configpath.DefaultConfigDir(),
		"docker-compose.yaml",
	)
}

// runDockerCompose executes a docker compose command with the given
// compose file and arguments, connecting stdout/stderr for live output.
func runDockerCompose(compose string, args ...string) error {
	fullArgs := make([]string, 0, len(args)+3)
	fullArgs = append(fullArgs, "compose", "-f", compose)
	fullArgs = append(fullArgs, args...)

	cmd := exec.Command("docker", fullArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	log.WithField("command", "docker "+strings.Join(fullArgs, " ")).
		Debug("Running docker compose")

	if err := cmd.Run(); err != nil {
		return fmt.Errorf(
			"docker compose %s failed: %w",
			strings.Join(args, " "),
			err,
		)
	}

	return nil
}

// printHealthStatus checks the server's /health endpoint and prints
// the result.
func printHealthStatus() {
	cfg, err := config.LoadClient(cfgFile)
	if err != nil {
		fmt.Println("Health: Unknown (config not loaded)")
		return
	}

	healthURL := strings.TrimRight(cfg.ServerURL(), "/") + "/health"

	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Get(healthURL) //nolint:noctx // simple health check
	if err != nil {
		fmt.Println("Health: Unreachable")
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusOK {
		fmt.Println("Health: Healthy")
	} else {
		fmt.Printf("Health: Unhealthy (HTTP %d)\n", resp.StatusCode)
	}
}

// printAuthStatus loads auth credentials and prints whether the user
// is authenticated against the configured proxy.
func printAuthStatus() {
	target := resolveAuthTargetFromConfig()
	if target == nil {
		fmt.Println("Auth: Not configured")
		return
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

	if store.IsAuthenticated() {
		fmt.Println("Auth: Authenticated")
	} else {
		fmt.Println("Auth: Not authenticated (run 'panda auth login')")
	}
}

// printProxyURL loads the config and prints the configured proxy URL.
func printProxyURL() {
	cfg, err := config.LoadClient(cfgFile)
	if err != nil {
		return
	}

	if cfg.Proxy.URL != "" {
		fmt.Printf("Proxy: %s\n", cfg.Proxy.URL)
	} else {
		fmt.Println("Proxy: Not configured")
	}
}
