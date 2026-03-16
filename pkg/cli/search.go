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
	searchRunbookTag      string
	searchRunbookLimit    int
	searchEIPStatus       string
	searchEIPCategory     string
	searchEIPType         string
	searchEIPLimit        int
)

var searchCmd = &cobra.Command{
	GroupID: groupWorkflow,
	Use:     "search",
	Short:   "Search examples, runbooks, and EIPs",
	Long: `Semantic search over query examples, investigation runbooks, and EIPs.

Examples:
  panda search examples "attestation participation"
  panda search runbooks "finality delay"
  panda search eips "account abstraction"`,
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

var searchEIPsCmd = &cobra.Command{
	Use:   "eips <query>",
	Short: "Search Ethereum Improvement Proposals",
	Args:  cobra.ExactArgs(1),
	RunE:  runSearchEIPs,
}

func init() {
	rootCmd.AddCommand(searchCmd)
	searchCmd.AddCommand(searchExamplesCmd)
	searchCmd.AddCommand(searchRunbooksCmd)
	searchCmd.AddCommand(searchEIPsCmd)

	searchExamplesCmd.Flags().StringVar(&searchExampleCategory, "category", "", "Filter by category")
	searchExamplesCmd.Flags().IntVar(&searchExampleLimit, "limit", 3, "Max results (default: 3, max: 10)")
	searchExamplesCmd.ValidArgsFunction = noCompletions

	searchRunbooksCmd.Flags().StringVar(&searchRunbookTag, "tag", "", "Filter by tag")
	searchRunbooksCmd.Flags().IntVar(&searchRunbookLimit, "limit", 3, "Max results (default: 3, max: 5)")
	searchRunbooksCmd.ValidArgsFunction = noCompletions

	searchEIPsCmd.Flags().StringVar(&searchEIPStatus, "status", "", "Filter by status (e.g., Final, Draft)")
	searchEIPsCmd.Flags().StringVar(&searchEIPCategory, "category", "", "Filter by category (e.g., Core, ERC)")
	searchEIPsCmd.Flags().StringVar(&searchEIPType, "type", "", "Filter by type (e.g., Standards Track)")
	searchEIPsCmd.Flags().IntVar(&searchEIPLimit, "limit", 5, "Max results (default: 5, max: 10)")
	searchEIPsCmd.ValidArgsFunction = noCompletions
}

func runSearchExamples(_ *cobra.Command, args []string) error {
	ctx := context.Background()

	response, err := searchExamples(ctx, args[0], searchExampleCategory, searchExampleLimit)
	if err != nil {
		return err
	}

	if isJSON() {
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

	if isJSON() {
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

func runSearchEIPs(_ *cobra.Command, args []string) error {
	ctx := context.Background()

	response, err := searchEIPs(
		ctx, args[0],
		searchEIPStatus, searchEIPCategory, searchEIPType,
		searchEIPLimit,
	)
	if err != nil {
		return err
	}

	if isJSON() {
		return printJSON(response)
	}

	if len(response.Results) == 0 {
		fmt.Println("No matching EIPs found.")
		return nil
	}

	for i, result := range response.Results {
		if i > 0 {
			fmt.Println("---")
		}

		fmt.Printf("EIP-%d: %s (score: %.2f)\n", result.Number, result.Title, result.SimilarityScore)

		if result.Description != "" {
			fmt.Printf("  %s\n", result.Description)
		}

		fmt.Printf("  Status: %s | Type: %s", result.Status, result.Type)
		if result.Category != "" {
			fmt.Printf(" | Category: %s", result.Category)
		}

		fmt.Println()
		fmt.Printf("  %s\n", result.URL)
	}

	return nil
}
