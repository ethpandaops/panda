package cli

import (
	"fmt"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/ethpandaops/panda/pkg/serverapi"
)

var (
	searchAllLimit        int
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
	Use:     "search [query]",
	Short:   "Search examples, runbooks, and EIPs",
	Long: `Semantic search over query examples, investigation runbooks, and EIPs.

When called with a query and no subcommand, searches all indices at once.

Examples:
  panda search "eip-4844"
  panda search examples "attestation participation"
  panda search runbooks "finality delay"
  panda search eips "account abstraction"`,
	Args: cobra.ArbitraryArgs,
	RunE: runSearchAll,
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

	searchCmd.Flags().IntVar(&searchAllLimit, "limit", 3, "Max results per index (default: 3)")
	searchCmd.ValidArgsFunction = noCompletions

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

func runSearchAll(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return cmd.Help()
	}

	query := strings.Join(args, " ")
	ctx := cmd.Context()

	var (
		examplesResp *serverapi.SearchExamplesResponse
		runbooksResp *serverapi.SearchRunbooksResponse
		eipsResp     *serverapi.SearchEIPsResponse
		examplesErr  error
		runbooksErr  error
		eipsErr      error
	)

	var wg sync.WaitGroup

	wg.Add(3)

	go func() {
		defer wg.Done()
		examplesResp, examplesErr = searchExamples(ctx, query, "", searchAllLimit)
	}()

	go func() {
		defer wg.Done()
		runbooksResp, runbooksErr = searchRunbooks(ctx, query, "", searchAllLimit)
	}()

	go func() {
		defer wg.Done()
		eipsResp, eipsErr = searchEIPs(ctx, query, "", "", "", searchAllLimit)
	}()

	wg.Wait()

	if isJSON() {
		if examplesErr != nil {
			return fmt.Errorf("searching examples: %w", examplesErr)
		}

		if runbooksErr != nil {
			return fmt.Errorf("searching runbooks: %w", runbooksErr)
		}

		if eipsErr != nil {
			return fmt.Errorf("searching eips: %w", eipsErr)
		}

		return printJSON(map[string]any{
			"query":    query,
			"examples": examplesResp,
			"runbooks": runbooksResp,
			"eips":     eipsResp,
		})
	}

	sections := 0

	if examplesErr == nil && len(examplesResp.Results) > 0 {
		if sections > 0 {
			fmt.Println()
		}

		sections++
		fmt.Printf("=== Examples (%d) ===\n\n", examplesResp.TotalMatches)
		printExampleResults(examplesResp.Results)
	}

	if runbooksErr == nil && len(runbooksResp.Results) > 0 {
		if sections > 0 {
			fmt.Println()
		}

		sections++
		fmt.Printf("=== Runbooks (%d) ===\n\n", runbooksResp.TotalMatches)
		printRunbookResults(runbooksResp.Results)
	}

	if eipsErr == nil && len(eipsResp.Results) > 0 {
		if sections > 0 {
			fmt.Println()
		}

		sections++
		fmt.Printf("=== EIPs (%d) ===\n\n", eipsResp.TotalMatches)
		printEIPResults(eipsResp.Results)
	}

	if sections == 0 {
		fmt.Println("No results found.")
	}

	var errs []string
	if examplesErr != nil {
		errs = append(errs, fmt.Sprintf("examples: %v", examplesErr))
	}

	if runbooksErr != nil {
		errs = append(errs, fmt.Sprintf("runbooks: %v", runbooksErr))
	}

	if eipsErr != nil {
		errs = append(errs, fmt.Sprintf("eips: %v", eipsErr))
	}

	if len(errs) > 0 {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "\nWarning: some searches failed: %s\n",
			strings.Join(errs, "; "))
	}

	return nil
}

func runSearchExamples(cmd *cobra.Command, args []string) error {
	response, err := searchExamples(cmd.Context(), args[0], searchExampleCategory, searchExampleLimit)
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

	printExampleResults(response.Results)

	return nil
}

func runSearchRunbooks(cmd *cobra.Command, args []string) error {
	response, err := searchRunbooks(cmd.Context(), args[0], searchRunbookTag, searchRunbookLimit)
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

	printRunbookResults(response.Results)

	return nil
}

func runSearchEIPs(cmd *cobra.Command, args []string) error {
	response, err := searchEIPs(
		cmd.Context(), args[0],
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

	printEIPResults(response.Results)

	return nil
}

func printExampleResults(results []*serverapi.SearchExampleResult) {
	for i, result := range results {
		if i > 0 {
			fmt.Println("---")
		}

		fmt.Printf("[%s] %s (score: %.2f)\n",
			result.CategoryName, result.ExampleName, result.SimilarityScore)
		fmt.Printf("  %s\n", result.Description)

		if result.TargetCluster != "" {
			fmt.Printf("  Cluster: %s\n", result.TargetCluster)
		}

		fmt.Printf("\n%s\n\n", result.Query)
	}
}

func printRunbookResults(results []*serverapi.SearchRunbookResult) {
	for i, result := range results {
		if i > 0 {
			fmt.Print("\n---\n\n")
		}

		fmt.Printf("%s (score: %.2f)\n", result.Name, result.SimilarityScore)
		fmt.Printf("  %s\n", result.Description)
		fmt.Printf("  Tags: %s\n", strings.Join(result.Tags, ", "))

		if len(result.Prerequisites) > 0 {
			fmt.Printf("  Prerequisites: %s\n",
				strings.Join(result.Prerequisites, ", "))
		}

		fmt.Printf("\n%s\n", result.Content)
	}
}

func printEIPResults(results []*serverapi.SearchEIPResult) {
	for i, result := range results {
		if i > 0 {
			fmt.Println("---")
		}

		fmt.Printf("EIP-%d: %s (score: %.2f)\n",
			result.Number, result.Title, result.SimilarityScore)

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
}
