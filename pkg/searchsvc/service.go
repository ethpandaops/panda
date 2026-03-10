package searchsvc

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/ethpandaops/mcp/pkg/module"
	"github.com/ethpandaops/mcp/pkg/resource"
	"github.com/ethpandaops/mcp/runbooks"
)

const (
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

type ExampleSearcher interface {
	Search(query string, limit int) ([]resource.SearchResult, error)
}

type RunbookSearcher interface {
	Search(query string, limit int) ([]resource.RunbookSearchResult, error)
}

type RunbookTagProvider interface {
	Tags() []string
}

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

type Service struct {
	exampleIndex ExampleSearcher
	moduleReg    *module.Registry
	runbookIndex RunbookSearcher
	runbookReg   RunbookTagProvider
}

func New(
	exampleIndex ExampleSearcher,
	moduleReg *module.Registry,
	runbookIndex RunbookSearcher,
	runbookReg RunbookTagProvider,
) *Service {
	return &Service{
		exampleIndex: exampleIndex,
		moduleReg:    moduleReg,
		runbookIndex: runbookIndex,
		runbookReg:   runbookReg,
	}
}

func NormalizeSearchType(searchType string) (string, error) {
	switch strings.TrimSpace(strings.ToLower(searchType)) {
	case SearchTypeExamples:
		return SearchTypeExamples, nil
	case SearchTypeRunbooks, SearchTypeNotebooks:
		return SearchTypeRunbooks, nil
	default:
		return "", fmt.Errorf("unsupported search type: %q", searchType)
	}
}

func (s *Service) SearchExamples(query, categoryFilter string, limit int) (*SearchExamplesResponse, error) {
	if s.exampleIndex == nil {
		return nil, fmt.Errorf("example search index not available")
	}

	limit = clampSearchLimit(limit, MaxExampleSearchLimit)

	examples := resource.GetQueryExamples(s.moduleReg)
	categories := make([]string, 0, len(examples))
	for key := range examples {
		categories = append(categories, key)
	}

	sort.Strings(categories)

	if categoryFilter != "" {
		if _, ok := examples[categoryFilter]; !ok {
			return nil, fmt.Errorf(
				"unknown category: %q. Available categories: %s",
				categoryFilter,
				strings.Join(categories, ", "),
			)
		}
	}

	searchLimit := limit
	if categoryFilter != "" {
		searchLimit = limit * exampleFilterOverscan
	}

	results, err := s.exampleIndex.Search(query, searchLimit)
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
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

	return &SearchExamplesResponse{
		Type:                SearchTypeExamples,
		Query:               query,
		CategoryFilter:      categoryFilter,
		TotalMatches:        len(searchResults),
		Results:             searchResults,
		AvailableCategories: categories,
	}, nil
}

func (s *Service) SearchRunbooks(query, tagFilter string, limit int) (*SearchRunbooksResponse, error) {
	if s.runbookIndex == nil || s.runbookReg == nil {
		return nil, fmt.Errorf("runbook search index not available")
	}

	limit = clampSearchLimit(limit, MaxRunbookSearchLimit)

	availableTags := s.runbookReg.Tags()
	sort.Strings(availableTags)

	if tagFilter != "" && !slices.Contains(availableTags, tagFilter) {
		return nil, fmt.Errorf(
			"unknown tag: %q. Available tags: %s",
			tagFilter,
			strings.Join(availableTags, ", "),
		)
	}

	searchLimit := limit
	if tagFilter != "" {
		searchLimit = limit * runbookFilterOverscan
	}

	results, err := s.runbookIndex.Search(query, searchLimit)
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
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

	return &SearchRunbooksResponse{
		Type:          SearchTypeRunbooks,
		Query:         query,
		TagFilter:     tagFilter,
		TotalMatches:  len(searchResults),
		Results:       searchResults,
		AvailableTags: availableTags,
	}, nil
}

func clampSearchLimit(limit, max int) int {
	if limit == 0 {
		return DefaultSearchLimit
	}

	if limit < 1 {
		return 1
	}

	if limit > max {
		return max
	}

	return limit
}

var (
	_ ExampleSearcher    = (*resource.ExampleIndex)(nil)
	_ RunbookSearcher    = (*resource.RunbookIndex)(nil)
	_ RunbookTagProvider = (*runbooks.Registry)(nil)
)
