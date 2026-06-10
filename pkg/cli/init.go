package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	dockerimage "github.com/docker/docker/api/types/image"
	dockerclient "github.com/docker/docker/client"
	"github.com/spf13/cobra"

	"github.com/ethpandaops/panda/internal/version"
	"github.com/ethpandaops/panda/pkg/config"
	"github.com/ethpandaops/panda/pkg/configpath"
	"github.com/ethpandaops/panda/pkg/proxy"
)

const (
	defaultProxyURL = "https://panda-proxy.ethpandaops.io"

	// imageRepo is the Docker repository for all published panda images.
	imageRepo = "ethpandaops/panda"

	// defaultSandboxImage and defaultServerImage are the floating tags used
	// as a fallback when the running binary has no concrete release version
	// (e.g. local dev builds).
	defaultSandboxImage = imageRepo + ":sandbox-latest"
	defaultServerImage  = imageRepo + ":server-latest"
)

var (
	initDir          = configpath.DefaultConfigDir()
	initForce        bool
	initProxyURL     string
	initSandboxImage string
	initServerImage  string
	initSkipDocker   bool
	initSkipAuth     bool
	initSkipStart    bool
)

var initCmd = &cobra.Command{
	GroupID: groupSetup,
	Use:     "init",
	Short:   "Set up panda and get running in one command",
	Long: `Initialize panda for first-time use.

This command runs the full setup:
  1. Checks that Docker and docker compose are available
  2. Pulls the server and sandbox container images
  3. Writes config and docker-compose files to ~/.config/panda/
  4. Authenticates against the proxy (opens browser)
  5. Starts the server container

Use --skip-docker to skip the Docker check and image pulls.
Use --skip-auth to skip the authentication step.
Use --skip-start to skip starting the server.`,
	RunE: runInit,
}

func init() {
	rootCmd.AddCommand(initCmd)
	initCmd.Flags().StringVar(&initDir, "dir", initDir, "target config directory")
	initCmd.Flags().BoolVar(&initForce, "force", false, "overwrite existing config files")
	initCmd.Flags().StringVar(&initProxyURL, "proxy-url", defaultProxyURL, "proxy URL for remote datasource access")
	initCmd.Flags().StringVar(&initSandboxImage, "sandbox-image", sandboxImageForVersion(version.Version), "sandbox container image to pull")
	initCmd.Flags().StringVar(&initServerImage, "server-image", serverImageForVersion(version.Version), "server container image to pull")
	initCmd.Flags().BoolVar(&initSkipDocker, "skip-docker", false, "skip Docker check and image pull")
	initCmd.Flags().BoolVar(&initSkipAuth, "skip-auth", false, "skip authentication step")
	initCmd.Flags().BoolVar(&initSkipStart, "skip-start", false, "skip starting the server")
	initCmd.Flags().BoolVar(&noBrowser, "no-browser", false,
		"manual auth flow for SSH/headless environments (auto-detected over SSH)")
}

func runInit(cmd *cobra.Command, _ []string) error {
	ctx := commandContext(cmd)

	// 1. Docker check and image pulls.
	if !initSkipDocker {
		if err := checkDockerAndPullImages(ctx); err != nil {
			return err
		}
	} else {
		fmt.Println("Skipping Docker check and image pulls (--skip-docker)")
	}

	// 2. Write config files.
	if err := os.MkdirAll(initDir, 0o755); err != nil {
		return fmt.Errorf("creating config directory %s: %w", initDir, err)
	}

	absConfigDir, err := filepath.Abs(initDir)
	if err != nil {
		return fmt.Errorf("resolving absolute path for %s: %w", initDir, err)
	}

	// Discover auth settings from the proxy (best-effort, falls back to defaults).
	fmt.Println("Discovering proxy auth settings...")

	discoverCtx, discoverCancel := context.WithTimeout(ctx, 15*time.Second)
	defer discoverCancel()

	authCfg := discoverProxyAuth(discoverCtx, initProxyURL)

	fmt.Printf("Auth issuer: %s\n", authCfg.IssuerURL)
	fmt.Printf("Auth client: %s\n", authCfg.ClientID)

	configContent := buildConfigTemplate(initProxyURL, initSandboxImage, authCfg)
	configPath := filepath.Join(initDir, "config.yaml")

	configCreated, err := writeConfigFile(configPath, configContent, initForce)
	if err != nil {
		return err
	}

	// Write user config placeholder (never overwritten, even with --force).
	userConfigPath := filepath.Join(initDir, config.UserConfigFilename)

	userConfigCreated, err := writeConfigFile(userConfigPath, config.UserConfigPlaceholder(), false)
	if err != nil {
		return err
	}

	composeContent := buildComposeTemplate(initServerImage, absConfigDir)
	composePath := filepath.Join(initDir, "docker-compose.yaml")

	composeCreated, err := writeConfigFile(composePath, composeContent, initForce)
	if err != nil {
		return err
	}

	// 3. Print config summary.
	fmt.Println()

	if configCreated > 0 {
		fmt.Printf("Config written to: %s\n", configPath)
	} else {
		fmt.Printf("Config already exists: %s (use --force to overwrite)\n", configPath)
	}

	if userConfigCreated > 0 {
		fmt.Printf("User config written to: %s\n", userConfigPath)
	}

	if composeCreated > 0 {
		fmt.Printf("Docker Compose written to: %s\n", composePath)
	} else {
		fmt.Printf("Docker Compose already exists: %s (use --force to overwrite)\n", composePath)
	}

	// 4. Authenticate against the proxy.
	if !initSkipAuth {
		fmt.Println()

		if skipped, err := initEnsureAuth(cmd); err != nil {
			return fmt.Errorf("authentication failed: %w", err)
		} else if skipped {
			fmt.Println("Already authenticated (credentials still valid)")
		}
	} else {
		fmt.Println("\nSkipping authentication (--skip-auth)")
	}

	// 5. Start the server.
	switch {
	case initSkipStart:
		fmt.Println("\nSkipping server start (--skip-start)")
		fmt.Println("Run 'panda server start' when ready")
	case initSkipDocker:
		fmt.Println("\nSkipping server start (Docker was skipped)")
		fmt.Println("Run 'panda server start' when Docker is available")
	default:
		fmt.Println()
		fmt.Println("Starting server...")

		if err := runComposeAndWait(
			commandContext(cmd),
			resolveComposeFile(),
			[]string{"up", "-d", "--force-recreate"},
			defaultServerHealthWaitTimeout,
		); err != nil {
			return fmt.Errorf("starting server: %w", err)
		}

		fmt.Println()
		fmt.Println("Server available at http://localhost:2480")
		fmt.Println("Run 'panda datasources' to list available datasources")
	}

	return nil
}

func writeConfigFile(path, content string, force bool) (int, error) {
	if _, err := os.Stat(path); err == nil && !force {
		return 0, nil
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return 0, fmt.Errorf("writing %s: %w", path, err)
	}

	return 1, nil
}

// initAuthConfig holds the auth settings discovered from the proxy or defaults.
type initAuthConfig struct {
	Mode      string
	IssuerURL string
	ClientID  string
}

// serverImageForVersion returns the published server image reference pinned to
// the given release version, falling back to server-latest for dev/unknown
// builds that have no corresponding published image.
func serverImageForVersion(v string) string {
	return pinnedImage("server", v, defaultServerImage)
}

// sandboxImageForVersion returns the published sandbox image reference pinned
// to the given release version, falling back to sandbox-latest for dev/unknown
// builds that have no corresponding published image.
func sandboxImageForVersion(v string) string {
	return pinnedImage("sandbox", v, defaultSandboxImage)
}

// pinnedImage builds a "<repo>:<component>-<version>" reference using the
// published tag convention (semver without a leading "v"). It returns the
// supplied fallback when v is not a concrete release version.
func pinnedImage(component, v, fallback string) string {
	tag := version.Clean(v)
	if !isReleaseVersion(tag) {
		return fallback
	}

	return fmt.Sprintf("%s:%s-%s", imageRepo, component, tag)
}

// isReleaseVersion reports whether v is a concrete published release version
// (e.g. "0.31.0") rather than a "dev"/"unknown" placeholder. Published release
// tags are semver and start with a digit.
func isReleaseVersion(v string) bool {
	return v != "" && v[0] >= '0' && v[0] <= '9'
}

func buildConfigTemplate(proxyURL, sandboxImage string, auth initAuthConfig) string {
	modeField := ""
	if auth.Mode != "" && auth.Mode != "oauth" {
		modeField = fmt.Sprintf("\n    mode: %q", auth.Mode)
	}

	return fmt.Sprintf(`# panda configuration
# Generated by 'panda init'. See https://github.com/ethpandaops/panda for all options.

server:
  host: "0.0.0.0"
  port: 2480
  base_url: "http://localhost:2480"
  sandbox_url: "http://panda-server:2480"

sandbox:
  image: %q
  network: "ethpandaops-panda-internal"
  host_shared_path: "/tmp/ethpandaops-panda-sandbox"

storage:
  base_dir: "/data/storage"
  cache_dir: "/data/cache"

proxy:
  url: %q
  auth:%s
    issuer_url: %q
    client_id: %q
`, sandboxImage, proxyURL, modeField, auth.IssuerURL, auth.ClientID)
}

// discoverProxyAuth fetches auth metadata from the proxy's /auth/metadata
// endpoint. Falls back to using the proxy URL as the issuer if discovery fails.
func discoverProxyAuth(ctx context.Context, proxyURL string) initAuthConfig {
	fallback := initAuthConfig{
		IssuerURL: proxyURL,
		ClientID:  defaultProxyAuthClientID,
	}

	metadataURL := strings.TrimRight(proxyURL, "/") + "/auth/metadata"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return fallback
	}

	httpClient := &http.Client{Timeout: 10 * time.Second}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fallback
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fallback
	}

	var meta proxy.AuthMetadataResponse
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return fallback
	}

	if meta.IssuerURL == "" || meta.ClientID == "" {
		return fallback
	}

	return initAuthConfig{
		Mode:      meta.Mode,
		IssuerURL: meta.IssuerURL,
		ClientID:  meta.ClientID,
	}
}

func buildComposeTemplate(serverImage, configDir string) string {
	dockerGID := detectDockerSocketGID()

	return fmt.Sprintf(`# panda server - Docker Compose configuration
# Generated by 'panda init'. Managed by 'panda server' commands.
#
# Do not edit: this file is regenerated on 'panda server update'.
# Put customizations (e.g. dns, extra volumes) in docker-compose.override.yaml
# in this directory; panda never touches that file.

services:
  panda-server:
    image: %s
    container_name: panda-server
    restart: unless-stopped
    group_add:
      - "%s"
    ports:
      - "127.0.0.1:2480:2480"
    extra_hosts:
      - "host.docker.internal:host-gateway"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - /tmp/ethpandaops-panda-sandbox:/tmp/ethpandaops-panda-sandbox
      - %s/config.yaml:/app/config.yaml:ro
      - %s/config.user.yaml:/app/config.user.yaml:ro
      - %s/credentials:/home/panda/.config/panda/credentials
      - panda-storage:/data/storage
      - panda-cache:/data/cache
    command: ["panda-server", "serve", "--config", "/app/config.yaml"]
    networks:
      - panda-internal

networks:
  panda-internal:
    name: ethpandaops-panda-internal
    driver: bridge

volumes:
  panda-storage:
  panda-cache:
`, serverImage, dockerGID, configDir, configDir, configDir)
}

// newDockerClient constructs a Docker client from the environment with API
// version negotiation enabled.
func newDockerClient() (*dockerclient.Client, error) {
	cli, err := dockerclient.NewClientWithOpts(
		dockerclient.FromEnv,
		dockerclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("creating docker client: %w", err)
	}

	return cli, nil
}

func checkDockerAndPullImages(ctx context.Context) error {
	fmt.Println("Checking Docker...")

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cli, err := newDockerClient()
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()

	if _, err := cli.Ping(pingCtx); err != nil {
		return fmt.Errorf("docker is not running: %w", err)
	}

	fmt.Println("Docker is available")

	// Check docker compose CLI.
	if err := checkDockerCompose(); err != nil {
		return err
	}

	// Pull server image.
	if err := pullImage(ctx, cli, initServerImage); err != nil {
		return err
	}

	// Pull sandbox image.
	if err := pullImage(ctx, cli, initSandboxImage); err != nil {
		return err
	}

	return nil
}

func checkDockerCompose() error {
	fmt.Println("Checking docker compose...")

	cmd := exec.Command("docker", "compose", "version")

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker compose is required but not available: %w", err)
	}

	fmt.Println("docker compose is available")

	return nil
}

func pullImage(ctx context.Context, cli *dockerclient.Client, image string) error {
	fmt.Printf("Pulling image %s...\n", image)

	pullCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	reader, err := cli.ImagePull(pullCtx, image, dockerimage.PullOptions{})
	if err != nil {
		return fmt.Errorf("pulling image %s: %w", image, err)
	}

	// Drain the pull output (progress JSON).
	_, _ = io.Copy(io.Discard, reader)
	_ = reader.Close()

	fmt.Printf("Image %s pulled successfully\n", image)

	return nil
}

// detectDockerSocketGID returns the group ID that owns /var/run/docker.sock
// as seen from inside a container. This is used to add the correct group to
// the server container so the non-root panda user can access the Docker socket.
//
// On Linux the host GID is correct. On macOS (Docker Desktop, OrbStack, etc.)
// the host GID is meaningless because Docker remaps ownership when mounting
// into the Linux VM. We probe the actual GID by running a lightweight
// container. Falls back to "0" (root) on any failure.
func detectDockerSocketGID() string {
	const dockerSocket = "/var/run/docker.sock"

	// Try the fast path: probe GID inside a container. This gives
	// the correct answer on both Linux and macOS.
	if gid, err := probeSocketGIDInContainer(dockerSocket); err == nil {
		return gid
	}

	// Fallback: stat the socket on the host.
	info, err := os.Stat(dockerSocket)
	if err != nil {
		return "0"
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "0"
	}

	return strconv.FormatUint(uint64(stat.Gid), 10)
}

// probeSocketGIDInContainer runs a minimal Alpine container to stat the
// Docker socket GID as the container runtime sees it.
func probeSocketGIDInContainer(socketPath string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx,
		"docker", "run", "--rm",
		"-v", socketPath+":/var/run/docker.sock",
		"alpine", "stat", "-c", "%g", "/var/run/docker.sock",
	)

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("probing socket GID: %w", err)
	}

	gid := strings.TrimSpace(string(out))
	if gid == "" {
		return "", fmt.Errorf("empty GID from container probe")
	}

	return gid, nil
}

// initEnsureAuth checks for existing valid credentials before starting the
// full OAuth login flow. Returns (true, nil) if auth was skipped because
// credentials are already valid, (false, nil) on successful fresh login,
// or (false, err) on failure.
func initEnsureAuth(cmd *cobra.Command) (bool, error) {
	target, err := resolveAuthTarget(commandContext(cmd))
	if err != nil {
		return false, err
	}

	if !target.enabled {
		fmt.Println("Proxy authentication is not enabled for the configured server.")
		return true, nil
	}

	client := newAuthClient(target, false)
	store := newAuthStore(target, client)

	// Try to get a valid access token (refreshes automatically if needed).
	if _, err := store.GetAccessToken(); err == nil {
		return true, nil
	}

	// No valid credentials — run the full login flow.
	fmt.Println("Authenticating...")

	return false, runAuthLogin(cmd, nil)
}
