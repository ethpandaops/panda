package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ethpandaops/panda/internal/version"
)

var versionCmd = &cobra.Command{
	GroupID: groupSetup,
	Use:     "version",
	Short:   "Print version information",
	RunE: func(_ *cobra.Command, _ []string) error {
		info := map[string]string{
			"version":    version.Version,
			"git_commit": version.GitCommit,
			"build_time": version.BuildTime,
		}

		if versionJSON || isJSON() {
			return printJSON(info)
		}

		fmt.Printf("panda version %s (commit: %s, built: %s)\n",
			version.Version, version.GitCommit, version.BuildTime)

		return nil
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
