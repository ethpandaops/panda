package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var (
	specsConstantFork  string
	specsConstantsFork string
	specsConstantsPref string
)

var specsCmd = &cobra.Command{
	GroupID: groupDiscovery,
	Use:     "specs",
	Short:   "Read consensus-specs protocol constants and documents",
	Long: `Read Ethereum consensus-specs protocol constants and documents.

Use this for protocol constants and spec definitions instead of inferring
values from observed chain data.

Examples:
  panda specs constant MAX_EFFECTIVE_BALANCE
  panda specs constant MAX_EFFECTIVE_BALANCE --fork phase0
  panda specs constants --prefix MAX_ --fork deneb
  panda specs document deneb beacon-chain
  panda search consensus-specs "fork choice"`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return cmd.Help()
	},
}

var specsConstantCmd = &cobra.Command{
	Use:     "constant <name>",
	Aliases: []string{"get-constant"},
	Short:   "Get a protocol constant from consensus specs",
	Args:    cobra.ExactArgs(1),
	RunE:    runSpecsConstant,
}

var specsConstantsCmd = &cobra.Command{
	Use:     "constants [prefix]",
	Aliases: []string{"list-constants"},
	Short:   "List protocol constants from consensus specs",
	Args:    cobra.MaximumNArgs(1),
	RunE:    runSpecsConstants,
}

var specsDocumentCmd = &cobra.Command{
	Use:     "document <fork> <topic>",
	Aliases: []string{"doc", "get-spec", "spec"},
	Short:   "Read a consensus spec document",
	Args:    cobra.ExactArgs(2),
	RunE:    runSpecsDocument,
}

func init() {
	rootCmd.AddCommand(specsCmd)
	specsCmd.AddCommand(specsConstantCmd, specsConstantsCmd, specsDocumentCmd)

	specsConstantCmd.Flags().StringVar(&specsConstantFork, "fork", "", "Consensus fork filter (e.g., phase0, deneb, electra)")
	specsConstantsCmd.Flags().StringVar(&specsConstantsFork, "fork", "", "Consensus fork filter (e.g., phase0, deneb, electra)")
	specsConstantsCmd.Flags().StringVar(&specsConstantsPref, "prefix", "", "Constant name prefix filter")

	specsCmd.ValidArgsFunction = noCompletions
	specsConstantCmd.ValidArgsFunction = noCompletions
	specsConstantsCmd.ValidArgsFunction = noCompletions
	specsDocumentCmd.ValidArgsFunction = noCompletions
}

func runSpecsConstant(cmd *cobra.Command, args []string) error {
	opArgs := map[string]any{"name": args[0]}
	if specsConstantFork != "" {
		opArgs["fork"] = specsConstantFork
	}

	response, err := runServerOperation(cmd, "specs.get_constant", opArgs)
	if err != nil {
		return err
	}

	if isJSON() {
		return printJSON(response.Data)
	}

	data, _ := response.Data.(map[string]any)
	printKeyValue([][2]string{
		{"Name", fmt.Sprint(data["name"])},
		{"Value", fmt.Sprint(data["value"])},
		{"Fork", fmt.Sprint(data["fork"])},
	})

	return nil
}

func runSpecsConstants(cmd *cobra.Command, args []string) error {
	prefix := specsConstantsPref
	if prefix == "" && len(args) > 0 {
		prefix = args[0]
	}

	opArgs := map[string]any{}
	if specsConstantsFork != "" {
		opArgs["fork"] = specsConstantsFork
	}
	if prefix != "" {
		opArgs["prefix"] = prefix
	}

	response, err := runServerOperation(cmd, "specs.list_constants", opArgs)
	if err != nil {
		return err
	}

	if isJSON() {
		return printJSON(response.Data)
	}

	data, _ := response.Data.(map[string]any)
	constants, _ := data["constants"].([]any)
	if len(constants) == 0 {
		fmt.Println("No matching consensus-spec constants found.")

		return nil
	}

	rows := make([][]string, 0, len(constants))
	for _, item := range constants {
		entry, _ := item.(map[string]any)
		rows = append(rows, []string{
			fmt.Sprint(entry["name"]),
			fmt.Sprint(entry["value"]),
			fmt.Sprint(entry["fork"]),
		})
	}

	printTable([]string{"NAME", "VALUE", "FORK"}, rows)

	return nil
}

func runSpecsDocument(cmd *cobra.Command, args []string) error {
	response, err := runServerOperation(cmd, "specs.get_spec", map[string]any{
		"fork":  args[0],
		"topic": args[1],
	})
	if err != nil {
		return err
	}

	if isJSON() {
		return printJSON(response.Data)
	}

	data, _ := response.Data.(map[string]any)
	title := strings.TrimSpace(fmt.Sprint(data["title"]))
	if title != "" && title != "<nil>" {
		fmt.Printf("# %s\n\n", title)
	}

	printKeyValue([][2]string{
		{"Fork", fmt.Sprint(data["fork"])},
		{"Topic", fmt.Sprint(data["topic"])},
		{"URL", fmt.Sprint(data["url"])},
	})

	content := strings.TrimSpace(fmt.Sprint(data["content"]))
	if content != "" && content != "<nil>" {
		fmt.Printf("\n%s\n", content)
	}

	return nil
}
