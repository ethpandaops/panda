package cli

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	dockerclient "github.com/docker/docker/client"
	"github.com/spf13/cobra"

	"github.com/ethpandaops/panda/internal/github"
	"github.com/ethpandaops/panda/internal/version"
)

const (
	// binaryName is the name of the CLI binary inside release archives.
	binaryName = "panda"

	// downloadTimeout is the maximum time for downloading release assets.
	downloadTimeout = 5 * time.Minute
)

var (
	upgradeCLIOnly    bool
	upgradeServerOnly bool
	upgradeYes        bool
)

var upgradeCmd = &cobra.Command{
	GroupID: groupSetup,
	Use:     "upgrade",
	Short:   "Upgrade panda CLI and server to the latest version",
	Long: `Upgrade the panda CLI binary, server container, sandbox container,
and docker-compose configuration to the latest release.

By default, upgrades everything. Use flags to limit scope:
  panda upgrade              # upgrade CLI + server + sandbox
  panda upgrade --cli-only   # only upgrade the CLI binary
  panda upgrade --server-only # only upgrade server/sandbox containers`,
	RunE: runUpgrade,
}

func init() {
	rootCmd.AddCommand(upgradeCmd)
	upgradeCmd.Flags().BoolVar(&upgradeCLIOnly, "cli-only", false,
		"only upgrade the CLI binary")
	upgradeCmd.Flags().BoolVar(&upgradeServerOnly, "server-only", false,
		"only upgrade server containers and compose file")
	upgradeCmd.Flags().BoolVarP(&upgradeYes, "yes", "y", false,
		"skip confirmation prompt")
}

func runUpgrade(_ *cobra.Command, _ []string) error {
	checker := github.NewReleaseChecker(github.RepoOwner, github.RepoName)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fmt.Println("Checking for updates...")

	release, err := checker.LatestRelease(ctx)
	if err != nil {
		return fmt.Errorf("checking for updates: %w", err)
	}

	if !version.IsNewer(version.Version, release.TagName) {
		fmt.Printf("Already up to date (%s).\n", version.Version)
		return nil
	}

	doCLI := !upgradeServerOnly
	doServer := !upgradeCLIOnly

	fmt.Printf("\nCurrent version: %s\n", version.Version)
	fmt.Printf("Latest version:  %s\n", release.TagName)
	fmt.Println()
	fmt.Println("Upgrade plan:")

	if doCLI {
		fmt.Printf("  CLI binary:        %s -> %s\n", version.Version, release.TagName)
	}

	if doServer {
		fmt.Printf("  Server container:  pull %s\n", defaultServerImage)
		fmt.Printf("  Sandbox container: pull %s\n", defaultSandboxImage)
		fmt.Println("  Docker Compose:    regenerate (picks up latest fixes)")
	}

	fmt.Println()

	if !upgradeYes {
		if !promptConfirm("Proceed?") {
			fmt.Println("Upgrade cancelled.")
			return nil
		}

		fmt.Println()
	}

	if doCLI {
		if err := downloadAndReplaceBinary(release); err != nil {
			return fmt.Errorf("upgrading CLI: %w", err)
		}
	}

	if doServer {
		// If we just replaced the CLI binary, shell out to the new
		// binary for the server upgrade. The running process still has
		// the old code in memory, so calling upgradeServer() in-process
		// would use the old compose template — missing any fixes the
		// new version added (e.g. group_add for Docker socket perms).
		if doCLI {
			if err := execNewBinary("server", "update"); err != nil {
				return fmt.Errorf("upgrading server: %w", err)
			}
		} else {
			if err := upgradeServer(); err != nil {
				return fmt.Errorf("upgrading server: %w", err)
			}
		}
	}

	fmt.Println()
	fmt.Println("Upgrade complete!")

	return nil
}

// downloadAndReplaceBinary downloads the latest CLI binary from a GitHub
// release and atomically replaces the currently running binary.
func downloadAndReplaceBinary(release *github.Release) error {
	asset, err := release.FindAsset(runtime.GOOS, runtime.GOARCH, binaryName)
	if err != nil {
		return err
	}

	fmt.Printf("Downloading %s %s for %s/%s...\n",
		binaryName, release.TagName, runtime.GOOS, runtime.GOARCH)

	tarballData, err := downloadURL(asset.BrowserDownloadURL)
	if err != nil {
		return fmt.Errorf("downloading binary: %w", err)
	}

	if err := verifyChecksum(release, asset.Name, tarballData); err != nil {
		return err
	}

	binaryData, err := extractFromTarGz(tarballData, binaryName)
	if err != nil {
		return fmt.Errorf("extracting binary: %w", err)
	}

	currentBinary, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding current binary: %w", err)
	}

	currentBinary, err = filepath.EvalSymlinks(currentBinary)
	if err != nil {
		return fmt.Errorf("resolving binary path: %w", err)
	}

	// Write the new binary next to the current one, then swap.
	newPath := currentBinary + ".new"

	if err := os.WriteFile(newPath, binaryData, 0o755); err != nil {
		return fmt.Errorf("writing new binary: %w", err)
	}

	if err := os.Rename(newPath, currentBinary); err != nil {
		// Rename can fail across filesystems — fall back to direct write.
		_ = os.Remove(newPath)

		if writeErr := os.WriteFile(currentBinary, binaryData, 0o755); writeErr != nil {
			return fmt.Errorf("replacing binary: %w", writeErr)
		}
	}

	fmt.Printf("CLI updated to %s\n", release.TagName)

	return nil
}

// upgradeServer regenerates the compose file, pulls images, and restarts.
func upgradeServer() error {
	fmt.Println("Regenerating docker-compose.yaml...")

	if err := regenerateComposeFile(); err != nil {
		return err
	}

	compose := resolveComposeFile()

	fmt.Println("Pulling server image...")

	if err := runDockerCompose(compose, "pull"); err != nil {
		return err
	}

	fmt.Println("Pulling sandbox image...")

	if err := pullSandboxImage(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to pull sandbox image: %v\n", err)
	}

	fmt.Println("Restarting server...")

	return runDockerCompose(compose, "up", "-d")
}

// regenerateComposeFile overwrites the docker-compose.yaml with the
// current template, picking up any fixes (e.g. group_add for Docker
// socket permissions).
func regenerateComposeFile() error {
	composePath := resolveComposeFile()
	configDir := filepath.Dir(composePath)

	absConfigDir, err := filepath.Abs(configDir)
	if err != nil {
		return fmt.Errorf("resolving config directory: %w", err)
	}

	content := buildComposeTemplate(defaultServerImage, absConfigDir)

	if err := os.WriteFile(composePath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("writing compose file: %w", err)
	}

	return nil
}

// execNewBinary runs the newly installed panda binary with the given
// arguments. This is used after a CLI self-update so that server-side
// operations (like compose regeneration) use the new binary's code
// rather than the old process still in memory.
func execNewBinary(args ...string) error {
	binary, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding binary: %w", err)
	}

	binary, err = filepath.EvalSymlinks(binary)
	if err != nil {
		return fmt.Errorf("resolving binary path: %w", err)
	}

	cmd := exec.Command(binary, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	return cmd.Run()
}

// pullSandboxImage pulls the default sandbox container image.
func pullSandboxImage() error {
	cli, err := dockerclient.NewClientWithOpts(
		dockerclient.FromEnv,
		dockerclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return fmt.Errorf("creating docker client: %w", err)
	}
	defer func() { _ = cli.Close() }()

	return pullImage(cli, defaultSandboxImage)
}

// promptConfirm prints a prompt and waits for Y/n input.
func promptConfirm(prompt string) bool {
	fmt.Printf("%s [Y/n] ", prompt)

	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return false
	}

	answer := strings.TrimSpace(strings.ToLower(scanner.Text()))

	return answer == "" || answer == "y" || answer == "yes"
}

// downloadURL fetches the content at the given URL.
func downloadURL(url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), downloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}

	return io.ReadAll(resp.Body)
}

// verifyChecksum downloads checksums.txt from the release and verifies
// the SHA256 of the given data matches the expected checksum for the
// given filename.
func verifyChecksum(
	release *github.Release,
	filename string,
	data []byte,
) error {
	checksumAsset, err := release.ChecksumsAsset()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Warning: no checksums.txt found, skipping verification")
		return nil
	}

	fmt.Println("Verifying checksum...")

	checksumData, err := downloadURL(checksumAsset.BrowserDownloadURL)
	if err != nil {
		return fmt.Errorf("downloading checksums: %w", err)
	}

	expected, err := findChecksum(string(checksumData), filename)
	if err != nil {
		return err
	}

	actual := sha256.Sum256(data)
	actualHex := hex.EncodeToString(actual[:])

	if actualHex != expected {
		return fmt.Errorf(
			"checksum mismatch for %s: expected %s, got %s",
			filename, expected, actualHex,
		)
	}

	return nil
}

// findChecksum parses a checksums.txt file and returns the hex digest
// for the given filename. Format: "<hex>  <filename>".
func findChecksum(checksums, filename string) (string, error) {
	for line := range strings.SplitSeq(checksums, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.Fields(line)
		if len(parts) == 2 && parts[1] == filename {
			return parts[0], nil
		}
	}

	return "", fmt.Errorf("no checksum found for %s", filename)
}

// extractFromTarGz extracts a single file by name from a gzipped tar archive.
func extractFromTarGz(data []byte, targetName string) ([]byte, error) {
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("opening gzip: %w", err)
	}
	defer func() { _ = gr.Close() }()

	tr := tar.NewReader(gr)

	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, fmt.Errorf("reading tar: %w", err)
		}

		if filepath.Base(header.Name) == targetName && header.Typeflag == tar.TypeReg {
			return io.ReadAll(tr)
		}
	}

	return nil, fmt.Errorf("file %q not found in archive", targetName)
}
