package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/mcp/pkg/plugin"
	"github.com/ethpandaops/mcp/pkg/resource"
	"github.com/ethpandaops/mcp/runbooks"
)

const (
	SearchToolName = "search"

	SearchTypeExamples  = "examples"
	SearchTypeRunbooks  = "runbooks"
	SearchTypeNotebooks = "notebooks"

	DefaultSearchLimit    = 3
	MaxExampleSearchLimit = 10
	MaxRunbookSearchLimit = 5
	MinExampleScore       = 0.3
	MinRunbookScore       = 0.25
	exampleFilterOverscan = 3
	runbookFilterOverscan = 2
)

const searchDescription = `Search indexed examples and runbooks using semantic search.

Use ` + "`type=\"examples\"`" + ` for query snippets (SQL, PromQL, LogQL) and ` + "`type=\"runbooks\"`" + ` for multi-step investigation procedures.

` + "`type=\"notebooks\"`" + ` is accepted as an alias for runbooks.

Examples:
- search(type="examples", query="block")
- search(type="examples", query="validator", category="validators")
- search(type="runbooks", query="network not finalizing")
- search(type="runbooks", query="slow clickhouse query", tag="performance")`

type SearchExampleResult struct {
	CategoryKey     string  `json:"category_key"`
	CategoryName    string  `json:"category_name"`
	ExampleName     string  `json:"example_name"`
	Description     string  `json:"description"`
	Query           string  `json:"query"`
	TargetCluster   string  `json:"target_cluster"`
	SimilarityScore float64 `json:"similarity_score"`
}

type SearchExamplesResponse struct {
	Type                string                 `json:"type"`
	Query               string                 `json:"query"`
	CategoryFilter      string                 `json:"category_filter,omitempty"`
	TotalMatches        int                    `json:"total_matches"`
	Results             []*SearchExampleResult `json:"results"`
	AvailableCategories []string               `json:"available_categories"`
}

type SearchRunbookResult struct {
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	Tags            []string `json:"tags"`
	Prerequisites   []string `json:"prerequisites"`
	Content         string   `json:"content"`
	FilePath        string   `json:"file_path"`
	SimilarityScore float64  `json:"similarity_score"`
}

type SearchRunbooksResponse struct {
	Type          string                 `json:"type"`
	Query         string                 `json:"query"`
	TagFilter     string                 `json:"tag_filter,omitempty"`
	TotalMatches  int                    `json:"total_matches"`
	Results       []*SearchRunbookResult `json:"results"`
	AvailableTags []string               `json:"available_tags"`
}

type searchHandler struct {
	log          logrus.FieldLogger
	exampleIndex *resource.ExampleIndex
	pluginReg    *plugin.Registry
	runbookIndex *resource.RunbookIndex
	runbookReg   *runbooks.Registry
}

func NewSearchTool(
	log logrus.FieldLogger,
	exampleIndex *resource.ExampleIndex,
	pluginReg *plugin.Registry,
	runbookIndex *resource.RunbookIndex,
	runbookReg *runbooks.Registry,
) Definition {
	h := &searchHandler{
		log:          log.WithField("tool", SearchToolName),
		exampleIndex: exampleIndex,
		pluginReg:    pluginReg,
		runbookIndex: runbookIndex,
		runbookReg:   runbookReg,
	}

	return Definition{
		Tool: mcp.Tool{
			Name:        SearchToolName,
			Description: searchDescription,
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"type": map[string]any{
						"type":        "string",
						"description": "Search target: 'examples' for query snippets or 'runbooks' for investigation procedures. 'notebooks' is accepted as an alias for 'runbooks'.",
						"enum":        []string{SearchTypeExamples, SearchTypeRunbooks, SearchTypeNotebooks},
					},
					"query": map[string]any{
						"type":        "string",
						"description": "Search term or phrase to find semantically similar content",
					},
					"category": map[string]any{
						"type":        "string",
						"description": "Optional for type='examples': filter to a specific category (e.g., 'attestations', 'block_events')",
					},
					"tag": map[string]any{
						"type":        "string",
						"description": "Optional for type='runbooks': filter to runbooks with a specific tag (e.g., 'finality', 'performance')",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum results to return. Defaults to 3. Max is 10 for examples and 5 for runbooks.",
						"minimum":     1,
						"maximum":     MaxExampleSearchLimit,
					},
				},
				Required: []string{"type", "query"},
			},
		},
		Handler: h.handle,
	}
}

func (h *searchHandler) handle(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	h.log.Debug("Handling search request")

	searchType, err := normalizeSearchType(request.GetString("type", ""))
	if err != nil {
		return CallToolError(err), nil
	}

	query := request.GetString("query", "")
	if query == "" {
		return CallToolError(fmt.Errorf("query is required and cannot be empty")), nil
	}

	switch searchType {
	case SearchTypeExamples:
		return h.searchExamples(request, query)
	case SearchTypeRunbooks:
		return h.searchRunbooks(request, query)
	default:
		return CallToolError(fmt.Errorf("unsupported search type: %q", searchType)), nil
	}
}

func (h *searchHandler) searchExamples(
	request mcp.CallToolRequest,
	query string,
) (*mcp.CallToolResult, error) {
	if h.exampleIndex == nil {
		return CallToolError(fmt.Errorf("example search index not available")), nil
	}

	if tag := request.GetString("tag", ""); tag != "" {
		return CallToolError(fmt.Errorf("tag is only supported for type=%q", SearchTypeRunbooks)), nil
	}

	categoryFilter := request.GetString("category", "")
	limit := clampSearchLimit(request.GetInt("limit", DefaultSearchLimit), MaxExampleSearchLimit)

	examples := resource.GetQueryExamples(h.pluginReg)
	categories := make([]string, 0, len(examples))
	for key := range examples {
		categories = append(categories, key)
	}

	sort.Strings(categories)

	if categoryFilter != "" {
		if _, ok := examples[categoryFilter]; !ok {
			return CallToolError(fmt.Errorf(
				"unknown category: %q. Available categories: %s",
				categoryFilter,
				strings.Join(categories, ", "),
			)), nil
		}
	}

	searchLimit := limit
	if categoryFilter != "" {
		searchLimit = limit * exampleFilterOverscan
	}

	results, err := h.exampleIndex.Search(query, searchLimit)
	if err != nil {
		return CallToolError(fmt.Errorf("search failed: %w", err)), nil
	}

	searchResults := make([]*SearchExampleResult, 0, len(results))
	for _, result := range results {
		if result.Score < MinExampleScore {
			continue
		}

		if categoryFilter != "" && result.CategoryKey != categoryFilter {
			continue
		}

		searchResults = append(searchResults, &SearchExampleResult{
			CategoryKey:     result.CategoryKey,
			CategoryName:    result.CategoryName,
			ExampleName:     result.Example.Name,
			Description:     result.Example.Description,
			Query:           result.Example.Query,
			TargetCluster:   result.Example.Cluster,
			SimilarityScore: result.Score,
		})

		if len(searchResults) >= limit {
			break
		}
	}

	response := &SearchExamplesResponse{
		Type:                SearchTypeExamples,
		Query:               query,
		CategoryFilter:      categoryFilter,
		TotalMatches:        len(searchResults),
		Results:             searchResults,
		AvailableCategories: categories,
	}

	data, err := json.MarshalIndent(response, "", "  ")
	if err != nil {
		return CallToolError(fmt.Errorf("marshaling response: %w", err)), nil
	}

	h.log.WithFields(logrus.Fields{
		"type":    SearchTypeExamples,
		"query":   query,
		"matches": len(searchResults),
	}).Debug("Search completed")

	return CallToolSuccess(string(data)), nil
}

func (h *searchHandler) searchRunbooks(
	request mcp.CallToolRequest,
	query string,
) (*mcp.CallToolResult, error) {
	if h.runbookIndex == nil || h.runbookReg == nil {
		return CallToolError(fmt.Errorf("runbook search index not available")), nil
	}

	if category := request.GetString("category", ""); category != "" {
		return CallToolError(fmt.Errorf("category is only supported for type=%q", SearchTypeExamples)), nil
	}

	tagFilter := request.GetString("tag", "")
	limit := clampSearchLimit(request.GetInt("limit", DefaultSearchLimit), MaxRunbookSearchLimit)

	availableTags := h.runbookReg.Tags()
	sort.Strings(availableTags)

	if tagFilter != "" && !slices.Contains(availableTags, tagFilter) {
		return CallToolError(fmt.Errorf(
			"unknown tag: %q. Available tags: %s",
			tagFilter,
			strings.Join(availableTags, ", "),
		)), nil
	}

	searchLimit := limit
	if tagFilter != "" {
		searchLimit = limit * runbookFilterOverscan
	}

	results, err := h.runbookIndex.Search(query, searchLimit)
	if err != nil {
		return CallToolError(fmt.Errorf("search failed: %w", err)), nil
	}

	searchResults := make([]*SearchRunbookResult, 0, len(results))
	for _, result := range results {
		if result.Score < MinRunbookScore {
			continue
		}

		if tagFilter != "" && !slices.Contains(result.Runbook.Tags, tagFilter) {
			continue
		}

		searchResults = append(searchResults, &SearchRunbookResult{
			Name:            result.Runbook.Name,
			Description:     result.Runbook.Description,
			Tags:            result.Runbook.Tags,
			Prerequisites:   result.Runbook.Prerequisites,
			Content:         result.Runbook.Content,
			FilePath:        result.Runbook.FilePath,
			SimilarityScore: result.Score,
		})

		if len(searchResults) >= limit {
			break
		}
	}

	response := &SearchRunbooksResponse{
		Type:          SearchTypeRunbooks,
		Query:         query,
		TagFilter:     tagFilter,
		TotalMatches:  len(searchResults),
		Results:       searchResults,
		AvailableTags: availableTags,
	}

	data, err := json.MarshalIndent(response, "", "  ")
	if err != nil {
		return CallToolError(fmt.Errorf("marshaling response: %w", err)), nil
	}

	h.log.WithFields(logrus.Fields{
		"type":    SearchTypeRunbooks,
		"query":   query,
		"matches": len(searchResults),
	}).Debug("Search completed")

	return CallToolSuccess(string(data)), nil
}

func clampSearchLimit(value int, max int) int {
	if value <= 0 {
		return DefaultSearchLimit
	}

	if value > max {
		return max
	}

	return value
}

func normalizeSearchType(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case SearchTypeExamples:
		return SearchTypeExamples, nil
	case SearchTypeRunbooks, SearchTypeNotebooks:
		return SearchTypeRunbooks, nil
	case "":
		return "", fmt.Errorf("type is required and cannot be empty")
	default:
		return "", fmt.Errorf(
			"unknown type: %q. Supported types: %s, %s",
			value,
			SearchTypeExamples,
			SearchTypeRunbooks,
		)
	}
}
