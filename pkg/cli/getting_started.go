package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var gettingStartedCmd = &cobra.Command{
	GroupID: groupDiscovery,
	Use:     "getting-started",
	Short:   "Show the getting started guide",
	Long: `Display the getting started guide: workflow, data discovery pointers, and
session rules. Per-dataset syntax rules live in 'panda datasets <name>'.

Examples:
  panda getting-started
  panda getting-started -o json`,
	RunE: runGettingStarted,
}

func init() {
	rootCmd.AddCommand(gettingStartedCmd)
}

func runGettingStarted(cmd *cobra.Command, _ []string) error {
	response, err := readResource(cmd.Context(), "panda://getting-started")
	if err != nil {
		return fmt.Errorf("reading getting-started guide: %w", err)
	}

	if isJSON() {
		return printJSON(response)
	}

	fmt.Print(response.Content)

	return nil
}
