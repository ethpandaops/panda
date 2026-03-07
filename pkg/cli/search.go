package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ethpandaops/mcp/pkg/app"
	"github.com/ethpandaops/mcp/pkg/config"
	"github.com/ethpandaops/mcp/pkg/resource"
)

var searchCmd = &cobra.Command{
	Use:   "search",
	Short: "Search examples, runbooks, and EIPs",
	Long: `Semantic search over query examples, investigation runbooks, and Ethereum Improvement Proposals.

Examples:
  ep search examples "attestation participation"
  ep search runbooks "finality delay"
  ep search eips "account abstraction"`,
}

// --- search examples ---

var (
	searchExCategory string
	searchExLimit    int
	searchExJSON     bool
)

var searchExamplesCmd = &cobra.Command{
	Use:   "examples <query>",
	Short: "Search query examples",
	Long: `Semantic search over ClickHouse, Prometheus, Loki, and Dora query examples.
Returns matching examples with SQL/PromQL/LogQL queries and similarity scores.

Examples:
  ep search examples "block count"
  ep search examples "client diversity" --category client_diversity
  ep search examples "attestation" --limit 5 --json`,
	Args: cobra.ExactArgs(1),
	RunE: runSearchExamples,
}

// --- search runbooks ---

var (
	searchRbTag   string
	searchRbLimit int
	searchRbJSON  bool
)

var searchRunbooksCmd = &cobra.Command{
	Use:   "runbooks <query>",
	Short: "Search investigation runbooks",
	Long: `Semantic search over procedural runbooks for multi-step investigations.
Returns matching runbooks with full content, prerequisites, and tags.

Examples:
  ep search runbooks "finality delay"
  ep search runbooks "validator" --tag performance
  ep search runbooks "sync" --limit 2 --json`,
	Args: cobra.ExactArgs(1),
	RunE: runSearchRunbooks,
}

// --- search eips ---

var (
	searchEIPStatus   string
	searchEIPCategory string
	searchEIPType     string
	searchEIPLimit    int
	searchEIPJSON     bool
)

var searchEIPsCmd = &cobra.Command{
	Use:   "eips <query>",
	Short: "Search Ethereum Improvement Proposals",
	Long: `Semantic search over Ethereum Improvement Proposals (EIPs).
Returns matching EIPs with metadata, status, and links to full content.

Examples:
  ep search eips "account abstraction"
  ep search eips "gas pricing" --category Core
  ep search eips "token" --status Final --json`,
	Args: cobra.ExactArgs(1),
	RunE: runSearchEIPs,
}

func init() {
	rootCmd.AddCommand(searchCmd)

	searchCmd.AddCommand(searchExamplesCmd)
	searchExamplesCmd.Flags().StringVar(&searchExCategory, "category", "", "Filter by category")
	searchExamplesCmd.Flags().IntVar(&searchExLimit, "limit", 3, "Max results (default: 3, max: 10)")
	searchExamplesCmd.Flags().BoolVar(&searchExJSON, "json", false, "Output in JSON format")

	searchCmd.AddCommand(searchRunbooksCmd)
	searchRunbooksCmd.Flags().StringVar(&searchRbTag, "tag", "", "Filter by tag")
	searchRunbooksCmd.Flags().IntVar(&searchRbLimit, "limit", 3, "Max results (default: 3, max: 5)")
	searchRunbooksCmd.Flags().BoolVar(&searchRbJSON, "json", false, "Output in JSON format")

	searchCmd.AddCommand(searchEIPsCmd)
	searchEIPsCmd.Flags().StringVar(&searchEIPStatus, "status", "", "Filter by status (Draft, Final, etc.)")
	searchEIPsCmd.Flags().StringVar(&searchEIPCategory, "category", "", "Filter by category (Core, ERC, etc.)")
	searchEIPsCmd.Flags().StringVar(&searchEIPType, "type", "", "Filter by type (Standards Track, Meta, etc.)")
	searchEIPsCmd.Flags().IntVar(&searchEIPLimit, "limit", 5, "Max results (default: 5, max: 10)")
	searchEIPsCmd.Flags().BoolVar(&searchEIPJSON, "json", false, "Output in JSON format")
}

func buildSearchApp(ctx context.Context) (*app.App, error) {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}

	a := app.New(log, cfg)
	if err := a.BuildSearchOnly(ctx); err != nil {
		return nil, fmt.Errorf("building app: %w", err)
	}

	return a, nil
}

func runSearchExamples(_ *cobra.Command, args []string) error {
	ctx := context.Background()

	a, err := buildSearchApp(ctx)
	if err != nil {
		return err
	}

	defer func() { _ = a.Stop(ctx) }()

	if a.ExampleIndex == nil {
		return fmt.Errorf("example search index not available")
	}

	limit := searchExLimit
	if limit < 1 {
		limit = 1
	} else if limit > 10 {
		limit = 10
	}

	// Fetch extra results if filtering by category.
	fetchLimit := limit
	if searchExCategory != "" {
		fetchLimit = limit * 3
	}

	results, err := a.ExampleIndex.Search(args[0], fetchLimit)
	if err != nil {
		return fmt.Errorf("searching: %w", err)
	}

	// Filter by category and score threshold.
	var filtered []resource.SearchResult
	for _, r := range results {
		if r.Score < 0.3 {
			continue
		}

		if searchExCategory != "" && r.CategoryKey != searchExCategory {
			continue
		}

		filtered = append(filtered, r)

		if len(filtered) >= limit {
			break
		}
	}

	if searchExJSON {
		return printJSON(map[string]any{
			"query":   args[0],
			"results": filtered,
		})
	}

	if len(filtered) == 0 {
		fmt.Println("No matching examples found.")

		return nil
	}

	for i, r := range filtered {
		if i > 0 {
			fmt.Println("---")
		}

		fmt.Printf("[%s] %s (score: %.2f)\n", r.CategoryName, r.Example.Name, r.Score)
		fmt.Printf("  %s\n", r.Example.Description)

		if r.Example.Cluster != "" {
			fmt.Printf("  Cluster: %s\n", r.Example.Cluster)
		}

		fmt.Printf("\n%s\n\n", r.Example.Query)
	}

	return nil
}

func runSearchRunbooks(_ *cobra.Command, args []string) error {
	ctx := context.Background()

	a, err := buildSearchApp(ctx)
	if err != nil {
		return err
	}

	defer func() { _ = a.Stop(ctx) }()

	if a.RunbookIndex == nil {
		return fmt.Errorf("runbook search index not available")
	}

	limit := searchRbLimit
	if limit < 1 {
		limit = 1
	} else if limit > 5 {
		limit = 5
	}

	fetchLimit := limit
	if searchRbTag != "" {
		fetchLimit = limit * 2
	}

	results, err := a.RunbookIndex.Search(args[0], fetchLimit)
	if err != nil {
		return fmt.Errorf("searching: %w", err)
	}

	// Filter by tag and score threshold.
	var filtered []resource.RunbookSearchResult
	for _, r := range results {
		if r.Score < 0.25 {
			continue
		}

		if searchRbTag != "" && !containsTag(r.Runbook.Tags, searchRbTag) {
			continue
		}

		filtered = append(filtered, r)

		if len(filtered) >= limit {
			break
		}
	}

	if searchRbJSON {
		return printJSON(map[string]any{
			"query":   args[0],
			"results": filtered,
		})
	}

	if len(filtered) == 0 {
		fmt.Println("No matching runbooks found.")

		return nil
	}

	for i, r := range filtered {
		if i > 0 {
			fmt.Print("\n===\n\n")
		}

		fmt.Printf("%s (score: %.2f)\n", r.Runbook.Name, r.Score)
		fmt.Printf("  %s\n", r.Runbook.Description)
		fmt.Printf("  Tags: %s\n", strings.Join(r.Runbook.Tags, ", "))

		if len(r.Runbook.Prerequisites) > 0 {
			fmt.Printf("  Prerequisites: %s\n", strings.Join(r.Runbook.Prerequisites, ", "))
		}

		fmt.Printf("\n%s\n", r.Runbook.Content)
	}

	return nil
}

func runSearchEIPs(_ *cobra.Command, args []string) error {
	ctx := context.Background()

	a, err := buildSearchApp(ctx)
	if err != nil {
		return err
	}

	defer func() { _ = a.Stop(ctx) }()

	if a.EIPIndex == nil {
		return fmt.Errorf("EIP search index not available")
	}

	limit := searchEIPLimit
	if limit < 1 {
		limit = 1
	} else if limit > 10 {
		limit = 10
	}

	fetchLimit := limit
	if searchEIPStatus != "" || searchEIPCategory != "" || searchEIPType != "" {
		fetchLimit = limit * 3
	}

	results, err := a.EIPIndex.Search(args[0], fetchLimit)
	if err != nil {
		return fmt.Errorf("searching: %w", err)
	}

	var filtered []resource.EIPSearchResult

	for _, r := range results {
		if r.Score < 0.25 {
			continue
		}

		if searchEIPStatus != "" && r.EIP.Status != searchEIPStatus {
			continue
		}

		if searchEIPCategory != "" && r.EIP.Category != searchEIPCategory {
			continue
		}

		if searchEIPType != "" && r.EIP.Type != searchEIPType {
			continue
		}

		filtered = append(filtered, r)

		if len(filtered) >= limit {
			break
		}
	}

	if searchEIPJSON {
		return printJSON(map[string]any{
			"query":   args[0],
			"results": filtered,
		})
	}

	if len(filtered) == 0 {
		fmt.Println("No matching EIPs found.")

		return nil
	}

	for i, r := range filtered {
		if i > 0 {
			fmt.Println("---")
		}

		fmt.Printf("EIP-%d: %s (score: %.2f)\n", r.EIP.Number, r.EIP.Title, r.Score)
		fmt.Printf("  %s\n", r.EIP.Description)
		fmt.Printf("  Status: %s | Type: %s", r.EIP.Status, r.EIP.Type)

		if r.EIP.Category != "" {
			fmt.Printf(" | Category: %s", r.EIP.Category)
		}

		fmt.Println()
		fmt.Printf("  %s\n\n", r.EIP.URL)
	}

	return nil
}

func containsTag(tags []string, target string) bool {
	for _, t := range tags {
		if strings.EqualFold(t, target) {
			return true
		}
	}

	return false
}
