package cli

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/ethpandaops/panda/pkg/serverapi"
)

var (
	searchAllLimit        int
	searchExampleCategory string
	searchExampleDataset  string
	searchExampleLimit    int
	searchRunbookTag      string
	searchRunbookLimit    int
	searchEIPStatus       string
	searchEIPCategory     string
	searchEIPType         string
	searchEIPLimit        int
	searchSpecsFork       string
	searchSpecsLimit      int
	searchExamplesQuery   string
	searchRunbooksQuery   string
	searchEIPsQuery       string
	searchSpecsQuery      string
)

var searchCmd = &cobra.Command{
	GroupID: groupWorkflow,
	Use:     "search [query]",
	Short:   "Search all indices; use 'search examples' for query patterns",
	Long: `Semantic search over query examples, investigation runbooks, EIPs, and consensus specs.

For data queries, start with 'panda search examples "<topic>"' to get SQL/API
patterns without unrelated protocol results. When called with a query and no
subcommand, this command searches all indices at once and prints compact
summaries for non-example results.

Examples:
  panda search examples "attestation participation"
  panda search runbooks "finality delay"
  panda search "eip-4844"
  panda search eips "account abstraction"
  panda search consensus-specs "MAX_EFFECTIVE_BALANCE"`,
	Args: cobra.ArbitraryArgs,
	RunE: runSearchAll,
}

var searchExamplesCmd = &cobra.Command{
	Use:   "examples <query...>",
	Short: "Search query examples",
	Args:  queryArgsOrFlag(&searchExamplesQuery),
	RunE:  runSearchExamples,
}

var searchRunbooksCmd = &cobra.Command{
	Use:   "runbooks <query...>",
	Short: "Search investigation runbooks",
	Args:  queryArgsOrFlag(&searchRunbooksQuery),
	RunE:  runSearchRunbooks,
}

var searchEIPsCmd = &cobra.Command{
	Use:   "eips <query...>",
	Short: "Search Ethereum Improvement Proposals",
	Args:  queryArgsOrFlag(&searchEIPsQuery),
	RunE:  runSearchEIPs,
}

var searchSpecsCmd = &cobra.Command{
	Use:     "consensus-specs <query...>",
	Aliases: []string{"specs"},
	Short:   "Search consensus-specs documents and protocol constants",
	Args:    queryArgsOrFlag(&searchSpecsQuery),
	RunE:    runSearchSpecs,
}

func init() {
	rootCmd.AddCommand(searchCmd)
	searchCmd.AddCommand(searchExamplesCmd)
	searchCmd.AddCommand(searchRunbooksCmd)
	searchCmd.AddCommand(searchEIPsCmd)
	searchCmd.AddCommand(searchSpecsCmd)

	searchCmd.Flags().IntVar(&searchAllLimit, "limit", 3, "Max results per index (default: 3)")
	searchCmd.ValidArgsFunction = noCompletions

	searchExamplesCmd.Flags().StringVar(&searchExamplesQuery, "query", "", "Query text (alternative to positional query)")
	searchExamplesCmd.Flags().StringVar(&searchExampleCategory, "category", "", "Filter by category")
	searchExamplesCmd.Flags().StringVar(&searchExampleDataset, "dataset", "", "Filter by dataset (names: panda datasets)")
	searchExamplesCmd.Flags().IntVar(&searchExampleLimit, "limit", 3, "Max results (default: 3, max: 10)")
	searchExamplesCmd.ValidArgsFunction = noCompletions

	searchRunbooksCmd.Flags().StringVar(&searchRunbooksQuery, "query", "", "Query text (alternative to positional query)")
	searchRunbooksCmd.Flags().StringVar(&searchRunbookTag, "tag", "", "Filter by tag")
	searchRunbooksCmd.Flags().IntVar(&searchRunbookLimit, "limit", 3, "Max results (default: 3, max: 5)")
	searchRunbooksCmd.ValidArgsFunction = noCompletions

	searchEIPsCmd.Flags().StringVar(&searchEIPsQuery, "query", "", "Query text (alternative to positional query)")
	searchEIPsCmd.Flags().StringVar(&searchEIPStatus, "status", "", "Filter by status (e.g., Final, Draft)")
	searchEIPsCmd.Flags().StringVar(&searchEIPCategory, "category", "", "Filter by category (e.g., Core, ERC)")
	searchEIPsCmd.Flags().StringVar(&searchEIPType, "type", "", "Filter by type (e.g., Standards Track)")
	searchEIPsCmd.Flags().IntVar(&searchEIPLimit, "limit", 5, "Max results (default: 5, max: 10)")
	searchEIPsCmd.ValidArgsFunction = noCompletions

	searchSpecsCmd.Flags().StringVar(&searchSpecsQuery, "query", "", "Query text (alternative to positional query)")
	searchSpecsCmd.Flags().StringVar(&searchSpecsFork, "fork", "", "Filter by consensus fork (e.g., deneb, electra)")
	searchSpecsCmd.Flags().IntVar(&searchSpecsLimit, "limit", 5, "Max results (default: 5, max: 10)")
	searchSpecsCmd.ValidArgsFunction = noCompletions
}

func queryArgsOrFlag(queryFlag *string) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if strings.TrimSpace(*queryFlag) != "" {
			return nil
		}

		if len(args) == 0 {
			return fmt.Errorf("requires a query; pass positional words or --query")
		}

		return nil
	}
}

func queryFromArgsOrFlag(args []string, queryFlag string) string {
	if strings.TrimSpace(queryFlag) != "" {
		return queryFlag
	}

	return strings.Join(args, " ")
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
		specsResp    *serverapi.SearchSpecsResponse
		examplesErr  error
		runbooksErr  error
		eipsErr      error
		specsErr     error
	)

	var wg sync.WaitGroup

	wg.Add(4)

	go func() {
		defer wg.Done()
		examplesResp, examplesErr = searchExamples(ctx, query, "", "", searchAllLimit)
	}()

	go func() {
		defer wg.Done()
		runbooksResp, runbooksErr = searchRunbooks(ctx, query, "", searchAllLimit)
	}()

	go func() {
		defer wg.Done()
		eipsResp, eipsErr = searchEIPs(ctx, query, "", "", "", searchAllLimit)
	}()

	go func() {
		defer wg.Done()
		specsResp, specsErr = searchSpecs(ctx, query, "", searchAllLimit)
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

		if specsErr != nil {
			return fmt.Errorf("searching specs: %w", specsErr)
		}

		return printJSON(map[string]any{
			"query":    query,
			"examples": examplesResp,
			"runbooks": compactRunbookResponse(runbooksResp),
			"eips":     eipsResp,
			"specs":    specsResp,
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
		printRunbookSummaries(runbooksResp.Results)
	}

	if eipsErr == nil && len(eipsResp.Results) > 0 {
		if sections > 0 {
			fmt.Println()
		}

		sections++
		fmt.Printf("=== EIPs (%d) ===\n\n", eipsResp.TotalMatches)
		printEIPResults(eipsResp.Results)
	}

	if specsErr == nil && specsResp != nil {
		specMatches := len(specsResp.Specs) + len(specsResp.Constants)
		if specMatches > 0 {
			if sections > 0 {
				fmt.Println()
			}

			sections++
			fmt.Printf("=== Consensus Specs (%d) ===\n\n", specMatches)
			printSpecResults(specsResp)
		}
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

	if specsErr != nil {
		errs = append(errs, fmt.Sprintf("specs: %v", specsErr))
	}

	if len(errs) > 0 {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "\nWarning: some searches failed: %s\n",
			strings.Join(errs, "; "))
	}

	return nil
}

func runSearchExamples(cmd *cobra.Command, args []string) error {
	query := queryFromArgsOrFlag(args, searchExamplesQuery)
	response, err := searchExamples(cmd.Context(), query, searchExampleCategory, searchExampleDataset, searchExampleLimit)
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
	printExampleUsageHints(response.Results)

	return nil
}

func runSearchRunbooks(cmd *cobra.Command, args []string) error {
	query := queryFromArgsOrFlag(args, searchRunbooksQuery)
	response, err := searchRunbooks(cmd.Context(), query, searchRunbookTag, searchRunbookLimit)
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
	query := queryFromArgsOrFlag(args, searchEIPsQuery)
	response, err := searchEIPs(
		cmd.Context(), query,
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

		if result.Target != "" {
			fmt.Printf("  Target: %s\n", result.Target)
		}

		if result.Dataset != "" {
			fmt.Printf("  Dataset: %s\n", result.Dataset)
		}

		fmt.Printf("\n%s\n\n", result.Query)
	}
}

func printExampleUsageHints(results []*serverapi.SearchExampleResult) {
	if len(results) == 0 {
		return
	}

	var hasDataset, hasSQL bool
	targets := make(map[string]struct{})
	for _, result := range results {
		if result == nil {
			continue
		}

		hasDataset = hasDataset || result.Dataset != ""
		hasSQL = hasSQL || looksLikeSQLExample(result.Query)
		if result.Target != "" {
			targets[result.Target] = struct{}{}
		}
	}

	fmt.Println("Tips:")
	fmt.Println("  Search examples are reusable patterns; replace placeholders and concrete network/time filters before executing.")
	if hasSQL {
		fmt.Println("  For SQL examples, Target is the ClickHouse datasource/cluster: panda clickhouse query-raw <Target> \"<SQL>\"")
	}
	if len(targets) > 1 {
		fmt.Println("  Results span multiple Targets; query each Target separately and combine bounded results with panda execute or another client-side step.")
	}
	if hasDataset {
		fmt.Println("  Dataset is a guide name, not a datasource or database. Read it for syntax/placement: panda datasets <Dataset>")
	}
}

func looksLikeSQLExample(query string) bool {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return false
	}

	lower := strings.ToLower(trimmed)
	for _, prefix := range []string{"select ", "with ", "show ", "describe ", "explain "} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}

	return false
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

func compactRunbookResponse(resp *serverapi.SearchRunbooksResponse) *serverapi.SearchRunbooksResponse {
	if resp == nil {
		return nil
	}

	compact := *resp
	compact.Results = make([]*serverapi.SearchRunbookResult, 0, len(resp.Results))

	for _, result := range resp.Results {
		if result == nil {
			continue
		}

		item := *result
		item.Content = ""
		compact.Results = append(compact.Results, &item)
	}

	return &compact
}

func printRunbookSummaries(results []*serverapi.SearchRunbookResult) {
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

		fmt.Println("  Full content: panda search runbooks \"<query>\"")
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

func runSearchSpecs(cmd *cobra.Command, args []string) error {
	query := queryFromArgsOrFlag(args, searchSpecsQuery)
	response, err := searchSpecs(cmd.Context(), query, searchSpecsFork, searchSpecsLimit)
	if err != nil {
		return err
	}

	if isJSON() {
		return printJSON(response)
	}

	if len(response.Specs) == 0 && len(response.Constants) == 0 {
		fmt.Println("No matching consensus specs found.")
		return nil
	}

	printSpecResults(response)

	return nil
}

func printSpecResults(response *serverapi.SearchSpecsResponse) {
	if len(response.Constants) > 0 {
		fmt.Println("Constants:")

		for _, c := range response.Constants {
			fmt.Printf("  %s = %s (fork: %s, score: %.2f)\n",
				c.Name, c.Value, c.Fork, c.SimilarityScore)
		}

		if len(response.Specs) > 0 {
			fmt.Println()
		}
	}

	for i, result := range response.Specs {
		if i > 0 {
			fmt.Println("---")
		}

		fmt.Printf("[%s] %s (score: %.2f)\n", result.Fork, result.Title, result.SimilarityScore)
		fmt.Printf("  Topic: %s\n", result.Topic)
		fmt.Printf("  %s\n", result.URL)
	}
}

func searchExamples(ctx context.Context, queryText, category, dataset string, limit int) (*serverapi.SearchExamplesResponse, error) {
	query := url.Values{"query": []string{queryText}}
	if category != "" {
		query.Set("category", category)
	}
	if dataset != "" {
		query.Set("dataset", dataset)
	}
	if limit > 0 {
		query.Set("limit", fmt.Sprintf("%d", limit))
	}

	var response serverapi.SearchExamplesResponse
	if err := serverGetJSON(ctx, "/api/v1/search/examples", query, &response); err != nil {
		return nil, err
	}

	return &response, nil
}

func searchRunbooks(ctx context.Context, queryText, tag string, limit int) (*serverapi.SearchRunbooksResponse, error) {
	query := url.Values{"query": []string{queryText}}
	if tag != "" {
		query.Set("tag", tag)
	}
	if limit > 0 {
		query.Set("limit", fmt.Sprintf("%d", limit))
	}

	var response serverapi.SearchRunbooksResponse
	if err := serverGetJSON(ctx, "/api/v1/search/runbooks", query, &response); err != nil {
		return nil, err
	}

	return &response, nil
}

func searchEIPs(
	ctx context.Context,
	queryText, status, category, eipType string,
	limit int,
) (*serverapi.SearchEIPsResponse, error) {
	query := url.Values{"query": []string{queryText}}

	if status != "" {
		query.Set("status", status)
	}

	if category != "" {
		query.Set("category", category)
	}

	if eipType != "" {
		query.Set("type", eipType)
	}

	if limit > 0 {
		query.Set("limit", fmt.Sprintf("%d", limit))
	}

	var response serverapi.SearchEIPsResponse
	if err := serverGetJSON(ctx, "/api/v1/search/eips", query, &response); err != nil {
		return nil, err
	}

	return &response, nil
}

func searchSpecs(
	ctx context.Context,
	queryText, fork string,
	limit int,
) (*serverapi.SearchSpecsResponse, error) {
	query := url.Values{"query": []string{queryText}}

	if fork != "" {
		query.Set("fork", fork)
	}

	if limit > 0 {
		query.Set("limit", fmt.Sprintf("%d", limit))
	}

	var response serverapi.SearchSpecsResponse
	if err := serverGetJSON(ctx, "/api/v1/search/consensus-specs", query, &response); err != nil {
		return nil, err
	}

	return &response, nil
}
