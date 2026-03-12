package cli

import (
	"github.com/spf13/cobra"
)

var gettingStartedCmd = &cobra.Command{
	GroupID: groupDiscovery,
	Use:     "getting-started",
	Short:   "Show the getting started guide",
	Long: `Display the getting started guide with workflow guidance, available tools,
resources, and critical syntax rules for querying Ethereum data.

This is the same content served to MCP clients via the
ethpandaops://getting-started resource.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runResourcesRead(cmd, []string{"ethpandaops://getting-started"})
	},
}

func init() {
	rootCmd.AddCommand(gettingStartedCmd)
}
