package cli

import (
	"context"
	"fmt"
	"io"
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

	"github.com/ethpandaops/panda/pkg/configpath"
)

const (
	defaultProxyURL      = "https://panda-proxy.ethpandaops.io"
	defaultSandboxImage  = "ethpandaops/panda:sandbox-latest"
	defaultServerImage   = "ethpandaops/panda:server-latest"
	defaultProxyClientID = "panda"
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
	Use:   "init",
	Short: "Set up panda and get running in one command",
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
	initCmd.Flags().StringVar(&initSandboxImage, "sandbox-image", defaultSandboxImage, "sandbox container image to pull")
	initCmd.Flags().StringVar(&initServerImage, "server-image", defaultServerImage, "server container image to pull")
	initCmd.Flags().BoolVar(&initSkipDocker, "skip-docker", false, "skip Docker check and image pull")
	initCmd.Flags().BoolVar(&initSkipAuth, "skip-auth", false, "skip authentication step")
	initCmd.Flags().BoolVar(&initSkipStart, "skip-start", false, "skip starting the server")
}

func runInit(_ *cobra.Command, _ []string) error {
	// 1. Docker check and image pulls.
	if !initSkipDocker {
		if err := checkDockerAndPullImages(); err != nil {
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

	configContent := buildConfigTemplate(initProxyURL, initSandboxImage)
	configPath := filepath.Join(initDir, "config.yaml")

	configCreated, err := writeConfigFile(configPath, configContent, initForce)
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

	if composeCreated > 0 {
		fmt.Printf("Docker Compose written to: %s\n", composePath)
	} else {
		fmt.Printf("Docker Compose already exists: %s (use --force to overwrite)\n", composePath)
	}

	// 4. Authenticate against the proxy.
	if !initSkipAuth {
		fmt.Println()
		fmt.Println("Authenticating...")

		if err := runAuthLogin(nil, nil); err != nil {
			return fmt.Errorf("authentication failed: %w", err)
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

		if err := runDockerCompose(resolveComposeFile(), "up", "-d"); err != nil {
			return fmt.Errorf("starting server: %w", err)
		}

		fmt.Println()
		fmt.Println("Server is starting at http://localhost:2480")
		fmt.Println("Run 'panda server status' to check health")
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

func buildConfigTemplate(proxyURL, sandboxImage string) string {
	return fmt.Sprintf(`# panda configuration
# Generated by 'panda init'. See https://github.com/ethpandaops/panda for all options.

server:
  host: "0.0.0.0"
  port: 2480
  base_url: "http://localhost:2480"
  sandbox_url: "http://panda-server:2480"
  transport: sse

sandbox:
  image: %q
  network: "ethpandaops-panda-internal"
  host_shared_path: "/tmp/ethpandaops-panda-sandbox"

storage:
  base_dir: "/data/storage"

proxy:
  url: %q
  auth:
    issuer_url: %q
    client_id: %q
`, sandboxImage, proxyURL, proxyURL, defaultProxyClientID)
}

func buildComposeTemplate(serverImage, configDir string) string {
	dockerGID := detectDockerSocketGID()

	return fmt.Sprintf(`# panda server - Docker Compose configuration
# Generated by 'panda init'. Managed by 'panda server' commands.

services:
  panda-server:
    image: %s
    container_name: panda-server
    restart: unless-stopped
    group_add:
      - "%s"
    ports:
      - "127.0.0.1:2480:2480"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - /tmp/ethpandaops-panda-sandbox:/tmp/ethpandaops-panda-sandbox
      - %s/config.yaml:/app/config.yaml:ro
      - %s/credentials:/home/panda/.config/panda/credentials
      - panda-storage:/data/storage
    command: ["serve", "--config", "/app/config.yaml"]
    networks:
      - panda-internal

networks:
  panda-internal:
    name: ethpandaops-panda-internal
    driver: bridge

volumes:
  panda-storage:
`, serverImage, dockerGID, configDir, configDir)
}

func checkDockerAndPullImages() error {
	fmt.Println("Checking Docker...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cli, err := dockerclient.NewClientWithOpts(
		dockerclient.FromEnv,
		dockerclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return fmt.Errorf("docker is required but could not create client: %w", err)
	}
	defer func() { _ = cli.Close() }()

	if _, err := cli.Ping(ctx); err != nil {
		return fmt.Errorf("docker is not running: %w", err)
	}

	fmt.Println("Docker is available")

	// Check docker compose CLI.
	if err := checkDockerCompose(); err != nil {
		return err
	}

	// Pull server image.
	if err := pullImage(cli, initServerImage); err != nil {
		return err
	}

	// Pull sandbox image.
	if err := pullImage(cli, initSandboxImage); err != nil {
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

func pullImage(cli *dockerclient.Client, image string) error {
	fmt.Printf("Pulling image %s...\n", image)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	reader, err := cli.ImagePull(ctx, image, dockerimage.PullOptions{})
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
