package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ethpandaops/mcp/pkg/configpath"
)

const defaultAppConfigTemplate = `# ethpandaops CLI configuration
server:
  url: "http://localhost:2480"
`

var (
	initDir   = configpath.DefaultConfigDir()
	initForce bool
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Create a default CLI config in your home config directory",
	Long: `Create starter config for ep in your home config directory.

By default this writes:
  ~/.config/ethpandaops/config.yaml`,
	RunE: runInit,
}

func init() {
	rootCmd.AddCommand(initCmd)
	initCmd.Flags().StringVar(&initDir, "dir", initDir, "target config directory")
	initCmd.Flags().BoolVar(&initForce, "force", false, "overwrite existing files")
}

func runInit(_ *cobra.Command, _ []string) error {
	if err := os.MkdirAll(initDir, 0o755); err != nil {
		return fmt.Errorf("creating config directory %s: %w", initDir, err)
	}

	appPath := filepath.Join(initDir, "config.yaml")
	created, err := writeConfigFile(appPath, defaultAppConfigTemplate, initForce)
	if err != nil {
		return err
	}

	fmt.Printf("Config directory: %s\n", initDir)
	if created == 0 {
		fmt.Println("No files written. Existing config files were left in place.")
	} else {
		fmt.Printf("Wrote %d file(s):\n", created)
		if _, err := os.Stat(appPath); err == nil {
			fmt.Printf("  %s\n", appPath)
		}
	}

	fmt.Println("Point server.url at a running mcp server, then run `ep datasources`.")
	fmt.Println("If the server requires auth, run `mcp auth login --issuer <server-url> --client-id ep` first.")

	return nil
}

func writeConfigFile(path, content string, force bool) (int, error) {
	if _, err := os.Stat(path); err == nil && !force {
		fmt.Printf("Skipping existing file: %s\n", path)
		return 0, nil
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return 0, fmt.Errorf("writing %s: %w", path, err)
	}

	return 1, nil
}
