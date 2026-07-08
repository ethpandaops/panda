package cli

import (
	"embed"
	"fmt"

	"github.com/spf13/cobra"
)

// The workflow docs are embedded in the CLI and never registered as server
// resources: the workflow engine has no MCP surface, so MCP clients neither
// list nor read these docs.
//
//go:embed workflowdocs/*.md
var workflowDocFiles embed.FS

// workflowDocsTopics maps a `panda workflow docs [topic]` topic to its
// embedded file.
var workflowDocsTopics = map[string]string{
	"":      "workflowdocs/guide.md",
	"guide": "workflowdocs/guide.md",
	"api":   "workflowdocs/api.md",
}

var workflowDocsCmd = &cobra.Command{
	Use:   "docs [topic]",
	Short: "Show the workflow-engine lifecycle guide and API cheat-sheet",
	Long: `Show the embedded workflow-engine documentation. No topic (or 'guide')
shows the lifecycle guide; 'api' shows the endpoint cheat-sheet.

Examples:
  panda workflow docs
  panda workflow docs guide
  panda workflow docs api`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		topic := ""
		if len(args) == 1 {
			topic = args[0]
		}

		path, ok := workflowDocsTopics[topic]
		if !ok {
			return fmt.Errorf("unknown docs topic %q; valid topics: guide, api", topic)
		}

		data, err := workflowDocFiles.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading embedded doc %s: %w", path, err)
		}

		if isJSON() {
			return printJSON(map[string]string{
				"topic":   topicOrGuide(topic),
				"content": string(data),
			})
		}

		fmt.Print(string(data))

		return nil
	},
}

// topicOrGuide normalizes the empty default topic to its real name.
func topicOrGuide(topic string) string {
	if topic == "" {
		return "guide"
	}

	return topic
}

func init() {
	workflowDocsCmd.ValidArgsFunction = func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"guide", "api"}, cobra.ShellCompDirectiveNoFileComp
	}
}
