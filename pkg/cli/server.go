package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	dockerclient "github.com/moby/moby/client"
	"github.com/spf13/cobra"

	"github.com/ethpandaops/panda/pkg/config"
	"github.com/ethpandaops/panda/pkg/configpath"
	"github.com/ethpandaops/panda/pkg/sandbox"
)

var (
	composeFile         string
	dockerComposeRunner = runDockerComposeContext
)

const defaultServerHealthWaitTimeout = 5 * time.Minute

var (
	serverHealthPollInterval     = 10 * time.Second
	serverHealthProgressInterval = 10 * time.Second
)

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

func runServerStart(cmd *cobra.Command, _ []string) error {
	fmt.Println("Starting server...")

	return runComposeAndWait(
		commandContext(cmd),
		resolveComposeFile(),
		[]string{"up", "-d", "--force-recreate"},
		defaultServerHealthWaitTimeout,
	)
}

func runServerStop(_ *cobra.Command, _ []string) error {
	// Clean up orphaned sandbox containers before compose down,
	// so the shared network can be removed cleanly.
	cleanupSandboxContainers()

	return runDockerCompose(resolveComposeFile(), "down")
}

func runServerRestart(cmd *cobra.Command, _ []string) error {
	fmt.Println("Restarting server...")

	return runComposeAndWait(
		commandContext(cmd),
		resolveComposeFile(),
		[]string{"restart"},
		defaultServerHealthWaitTimeout,
	)
}

func runServerStatus(cmd *cobra.Command, _ []string) error {
	// Show container status.
	if err := runDockerCompose(resolveComposeFile(), "ps"); err != nil {
		return err
	}

	fmt.Println()

	// Show server health.
	printHealthStatus(commandContext(cmd))

	// Show auth status.
	printAuthStatus()

	// Show proxy URL from config.
	printProxyURL()

	return nil
}

type serverHealthConfigError struct {
	err error
}

func (e *serverHealthConfigError) Error() string {
	return fmt.Sprintf("load client config: %v", e.err)
}

func (e *serverHealthConfigError) Unwrap() error {
	return e.err
}

type serverHealthRequestError struct {
	err error
}

func (e *serverHealthRequestError) Error() string {
	return fmt.Sprintf("check server health: %v", e.err)
}

func (e *serverHealthRequestError) Unwrap() error {
	return e.err
}

type serverHealthStatusError struct {
	statusCode int
}

func (e *serverHealthStatusError) Error() string {
	return fmt.Sprintf("server health returned HTTP %d", e.statusCode)
}

func checkServerHealth(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	cfg, err := config.LoadClient(cfgFile)
	if err != nil {
		return &serverHealthConfigError{err: err}
	}

	healthURL := strings.TrimRight(cfg.ServerURL(), "/") + "/health"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		return &serverHealthRequestError{err: err}
	}

	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Do(req)
	if err != nil {
		return &serverHealthRequestError{err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return &serverHealthStatusError{statusCode: resp.StatusCode}
	}

	return nil
}

func waitForServerHealth(ctx context.Context, timeout time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}

	if timeout <= 0 {
		timeout = defaultServerHealthWaitTimeout
	}

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	deadline := time.Now().Add(timeout)
	nextProgressAt := time.Now().Add(serverHealthProgressInterval)
	var lastErr error

	for {
		err := checkServerHealth(waitCtx)
		if err == nil {
			return nil
		}

		var configErr *serverHealthConfigError
		if errors.As(err, &configErr) {
			return fmt.Errorf("cannot check server health: %w", err)
		}

		lastErr = err

		now := time.Now()
		if !now.Before(nextProgressAt) {
			remaining := max(time.Until(deadline).Round(time.Second), 0)

			fmt.Printf("Still waiting for server to become healthy... (%s remaining)\n", remaining)
			nextProgressAt = now.Add(serverHealthProgressInterval)
		}

		timer := time.NewTimer(serverHealthPollInterval)
		select {
		case <-waitCtx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}

			if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf(
					"server did not become healthy within %s (last check: %v). Check logs with 'panda server logs'",
					timeout,
					lastErr,
				)
			}

			return fmt.Errorf("server health wait canceled: %w", waitCtx.Err())
		case <-timer.C:
		}
	}
}

func runComposeAndWait(ctx context.Context, composeFile string, composeArgs []string, timeout time.Duration) error {
	if err := dockerComposeRunner(ctx, composeFile, composeArgs...); err != nil {
		return err
	}

	fmt.Println("Waiting for server to become healthy...")

	if err := waitForServerHealth(ctx, timeout); err != nil {
		return err
	}

	fmt.Println("Server ready.")

	return nil
}

func commandContext(cmd *cobra.Command) context.Context {
	if cmd != nil && cmd.Context() != nil {
		return cmd.Context()
	}

	return context.Background()
}

func runServerLogs(_ *cobra.Command, _ []string) error {
	return runDockerCompose(resolveComposeFile(), "logs", "-f")
}

func runServerUpdate(cmd *cobra.Command, _ []string) error {
	return upgradeServer(commandContext(cmd))
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

// composeOverrideFile returns the path to a user-managed
// docker-compose.override.yaml next to the given compose file, or "" if
// none exists. The override file is never written by panda, so it is the
// place for host-specific customizations (e.g. dns) that must survive the
// compose file regeneration done by 'panda server update'.
func composeOverrideFile(compose string) string {
	override := filepath.Join(filepath.Dir(compose), "docker-compose.override.yaml")

	if _, err := os.Stat(override); err != nil {
		return ""
	}

	return override
}

// runDockerCompose executes a docker compose command with the given
// compose file and arguments, connecting stdout/stderr for live output.
func runDockerCompose(compose string, args ...string) error {
	return runDockerComposeContext(context.Background(), compose, args...)
}

func runDockerComposeContext(ctx context.Context, compose string, args ...string) error {
	if ctx == nil {
		ctx = context.Background()
	}

	fullArgs := make([]string, 0, len(args)+5)
	fullArgs = append(fullArgs, "compose", "-f", compose)

	if override := composeOverrideFile(compose); override != "" {
		fullArgs = append(fullArgs, "-f", override)
	}

	fullArgs = append(fullArgs, args...)

	cmd := exec.CommandContext(ctx, "docker", fullArgs...)
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
func printHealthStatus(ctx context.Context) {
	err := checkServerHealth(ctx)
	if err == nil {
		fmt.Println("Health: Healthy")
		return
	}

	var configErr *serverHealthConfigError
	if errors.As(err, &configErr) {
		fmt.Println("Health: Unknown (config not loaded)")
		return
	}

	var statusErr *serverHealthStatusError
	if errors.As(err, &statusErr) {
		fmt.Printf("Health: Unhealthy (HTTP %d)\n", statusErr.statusCode)
		return
	}

	fmt.Println("Health: Unreachable")
}

// printAuthStatus loads auth credentials and prints whether the user
// is authenticated against the configured proxy.
func printAuthStatus() {
	target := resolveAuthTargetFromConfig()
	if target == nil {
		fmt.Println("Auth: Not configured")
		return
	}

	client := newAuthClient(target, false)
	store := newAuthStore(target, client)

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

// restartServerIfRunning restarts the panda server container if the compose
// file exists and the server is currently reachable. This is called after
// auth login to ensure the running server picks up new credentials.
func restartServerIfRunning(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}

	compose := resolveComposeFile()
	if _, err := os.Stat(compose); os.IsNotExist(err) {
		return
	}

	healthCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	if err := checkServerHealth(healthCtx); err != nil {
		return
	}

	fmt.Println("Restarting server to pick up new credentials...")

	if err := runComposeAndWait(
		ctx,
		compose,
		[]string{"restart"},
		defaultServerHealthWaitTimeout,
	); err != nil {
		log.WithError(err).Warn("Failed to restart server")
	}
}

// cleanupSandboxContainers removes any sandbox containers managed by panda.
// This is best-effort — failures are logged but do not block server stop.
func cleanupSandboxContainers() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cli, err := newDockerClient()
	if err != nil {
		return
	}
	defer func() { _ = cli.Close() }()

	filterArgs := make(dockerclient.Filters).Add("label", sandbox.LabelManaged+"=true")

	list, err := cli.ContainerList(ctx, dockerclient.ContainerListOptions{
		All:     true,
		Filters: filterArgs,
	})
	if err != nil {
		return
	}

	if len(list.Items) == 0 {
		return
	}

	fmt.Printf("Cleaning up %d sandbox container(s)...\n", len(list.Items))

	for _, c := range list.Items {
		if _, err := cli.ContainerRemove(ctx, c.ID, dockerclient.ContainerRemoveOptions{Force: true}); err != nil {
			log.WithField("container", c.ID[:12]).WithError(err).Warn("Failed to remove sandbox container")
		}
	}
}
