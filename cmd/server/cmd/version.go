package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ethpandaops/panda/internal/version"
)

var versionJSON bool

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version information",
	Run:   runVersion,
}

func init() {
	rootCmd.AddCommand(versionCmd)
	versionCmd.Flags().BoolVar(&versionJSON, "json", false, "Output in JSON format")
}

func runVersion(_ *cobra.Command, _ []string) {
	info := map[string]string{
		"version":    version.Version,
		"git_commit": version.GitCommit,
		"build_time": version.BuildTime,
	}

	if versionJSON {
		data, _ := json.MarshalIndent(info, "", "  ")
		fmt.Println(string(data))
	} else {
		fmt.Printf("panda-server version %s (commit: %s, built: %s)\n",
			version.Version, version.GitCommit, version.BuildTime)
	}
}
