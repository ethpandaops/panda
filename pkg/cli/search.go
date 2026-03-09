package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var (
	searchExampleCategory string
	searchExampleLimit    int
	searchExampleJSON     bool
	searchRunbookTag      string
	searchRunbookLimit    int
	searchRunbookJSON     bool
)

var searchCmd = &cobra.Command{
	Use:   "search",
	Short: "Search examples and runbooks",
	Long: `Semantic search over query examples and investigation runbooks.

Examples:
  ep search examples "attestation participation"
  ep search runbooks "finality delay"`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return cmd.Help()
	},
}

var searchExamplesCmd = &cobra.Command{
	Use:   "examples <query>",
	Short: "Search query examples",
	Args:  cobra.ExactArgs(1),
	RunE:  runSearchExamples,
}

var searchRunbooksCmd = &cobra.Command{
	Use:   "runbooks <query>",
	Short: "Search investigation runbooks",
	Args:  cobra.ExactArgs(1),
	RunE:  runSearchRunbooks,
}

func init() {
	rootCmd.AddCommand(searchCmd)
	searchCmd.AddCommand(searchExamplesCmd)
	searchCmd.AddCommand(searchRunbooksCmd)

	searchExamplesCmd.Flags().StringVar(&searchExampleCategory, "category", "", "Filter by category")
	searchExamplesCmd.Flags().IntVar(&searchExampleLimit, "limit", 3, "Max results (default: 3, max: 10)")
	searchExamplesCmd.Flags().BoolVar(&searchExampleJSON, "json", false, "Output in JSON format")

	searchRunbooksCmd.Flags().StringVar(&searchRunbookTag, "tag", "", "Filter by tag")
	searchRunbooksCmd.Flags().IntVar(&searchRunbookLimit, "limit", 3, "Max results (default: 3, max: 5)")
	searchRunbooksCmd.Flags().BoolVar(&searchRunbookJSON, "json", false, "Output in JSON format")
}

func runSearchExamples(_ *cobra.Command, args []string) error {
	ctx := context.Background()

	response, err := searchExamples(ctx, args[0], searchExampleCategory, searchExampleLimit)
	if err != nil {
		return err
	}

	if searchExampleJSON {
		return printJSON(response)
	}

	if len(response.Results) == 0 {
		fmt.Println("No matching examples found.")
		return nil
	}

	for i, result := range response.Results {
		if i > 0 {
			fmt.Println("---")
		}

		fmt.Printf("[%s] %s (score: %.2f)\n", result.CategoryName, result.ExampleName, result.SimilarityScore)
		fmt.Printf("  %s\n", result.Description)
		if result.TargetCluster != "" {
			fmt.Printf("  Cluster: %s\n", result.TargetCluster)
		}
		fmt.Printf("\n%s\n\n", result.Query)
	}

	return nil
}

func runSearchRunbooks(_ *cobra.Command, args []string) error {
	ctx := context.Background()

	response, err := searchRunbooks(ctx, args[0], searchRunbookTag, searchRunbookLimit)
	if err != nil {
		return err
	}

	if searchRunbookJSON {
		return printJSON(response)
	}

	if len(response.Results) == 0 {
		fmt.Println("No matching runbooks found.")
		return nil
	}

	for i, result := range response.Results {
		if i > 0 {
			fmt.Print("\n===\n\n")
		}

		fmt.Printf("%s (score: %.2f)\n", result.Name, result.SimilarityScore)
		fmt.Printf("  %s\n", result.Description)
		fmt.Printf("  Tags: %s\n", strings.Join(result.Tags, ", "))
		if len(result.Prerequisites) > 0 {
			fmt.Printf("  Prerequisites: %s\n", strings.Join(result.Prerequisites, ", "))
		}
		fmt.Printf("\n%s\n", result.Content)
	}

	return nil
}
