package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ethpandaops/mcp/pkg/app"
	"github.com/ethpandaops/mcp/pkg/config"
	"github.com/ethpandaops/mcp/pkg/searchsvc"
)

var searchCmd = &cobra.Command{
	Use:   "search",
	Short: "Search examples and runbooks",
	Long: `Semantic search over query examples and investigation runbooks.

Examples:
  ep search examples "attestation participation"
  ep search runbooks "finality delay"`,
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
}

func buildSearchApp(ctx context.Context) (*app.App, error) {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}

	// Search only needs plugins (for examples) + embedding model.
	// Use BuildLight for proxy+plugins, then the indices are built via full Build.
	// Actually we need the full build for search indices.
	a := app.New(log, cfg)
	if err := a.Build(ctx); err != nil {
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

	service := searchsvc.New(a.ExampleIndex, a.PluginRegistry, a.RunbookIndex, a.RunbookRegistry)
	response, err := service.SearchExamples(args[0], searchExCategory, searchExLimit)
	if err != nil {
		return err
	}

	if searchExJSON {
		return printJSON(map[string]any{
			"query":   args[0],
			"results": response.Results,
		})
	}

	if len(response.Results) == 0 {
		fmt.Println("No matching examples found.")

		return nil
	}

	for i, r := range response.Results {
		if i > 0 {
			fmt.Println("---")
		}

		fmt.Printf("[%s] %s (score: %.2f)\n", r.CategoryName, r.ExampleName, r.SimilarityScore)
		fmt.Printf("  %s\n", r.Description)

		if r.TargetCluster != "" {
			fmt.Printf("  Cluster: %s\n", r.TargetCluster)
		}

		fmt.Printf("\n%s\n\n", r.Query)
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

	service := searchsvc.New(a.ExampleIndex, a.PluginRegistry, a.RunbookIndex, a.RunbookRegistry)
	response, err := service.SearchRunbooks(args[0], searchRbTag, searchRbLimit)
	if err != nil {
		return err
	}

	if searchRbJSON {
		return printJSON(map[string]any{
			"query":   args[0],
			"results": response.Results,
		})
	}

	if len(response.Results) == 0 {
		fmt.Println("No matching runbooks found.")

		return nil
	}

	for i, r := range response.Results {
		if i > 0 {
			fmt.Print("\n===\n\n")
		}

		fmt.Printf("%s (score: %.2f)\n", r.Name, r.SimilarityScore)
		fmt.Printf("  %s\n", r.Description)
		fmt.Printf("  Tags: %s\n", strings.Join(r.Tags, ", "))

		if len(r.Prerequisites) > 0 {
			fmt.Printf("  Prerequisites: %s\n", strings.Join(r.Prerequisites, ", "))
		}

		fmt.Printf("\n%s\n", r.Content)
	}

	return nil
}
