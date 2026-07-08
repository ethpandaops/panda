package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// workflowDocsTopics maps a `panda workflow docs [topic]` topic to its resource URI.
var workflowDocsTopics = map[string]string{
	"":      "workflow://guide",
	"guide": "workflow://guide",
	"api":   "workflow://api",
}

var workflowDocsCmd = &cobra.Command{
	Use:   "docs [topic]",
	Short: "Show the workflow-engine lifecycle guide and API cheat-sheet",
	Long: `Show the embedded workflow-engine documentation, served from a server resource
(like 'panda docs'). No topic (or 'guide') shows the lifecycle guide; 'api'
shows the endpoint cheat-sheet.

Examples:
  panda workflow docs
  panda workflow docs guide
  panda workflow docs api`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		topic := ""
		if len(args) == 1 {
			topic = args[0]
		}

		uri, ok := workflowDocsTopics[topic]
		if !ok {
			return fmt.Errorf("unknown docs topic %q; valid topics: guide, api", topic)
		}

		response, err := readResource(cmd.Context(), uri)
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSON(response)
		}

		fmt.Print(response.Content)

		return nil
	},
}

func init() {
	workflowDocsCmd.ValidArgsFunction = func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"guide", "api"}, cobra.ShellCompDirectiveNoFileComp
	}
}
