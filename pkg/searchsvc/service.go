package searchsvc

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/resource"
	"github.com/ethpandaops/panda/runbooks"
)

const (
	SearchTypeExamples  = "examples"
	SearchTypeRunbooks  = "runbooks"
	SearchTypeNotebooks = "notebooks"
	SearchTypeEIPs      = "eips"

	DefaultSearchLimit    = 3
	MaxExampleSearchLimit = 10
	MaxRunbookSearchLimit = 5
	MaxEIPSearchLimit     = 10
	MinExampleScore       = 0.3
	MinRunbookScore       = 0.25
	MinEIPScore           = 0.25
	exampleFilterOverscan = 3
	runbookFilterOverscan = 2
	eipFilterOverscan     = 3
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

// EIPSearcher provides semantic search over EIPs.
type EIPSearcher interface {
	Search(query string, limit int) ([]resource.EIPSearchResult, error)
}

// EIPMetadataProvider provides filter metadata for EIP search.
type EIPMetadataProvider interface {
	Statuses() []string
	Categories() []string
	Types() []string
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

// SearchEIPResult represents a single EIP search result.
type SearchEIPResult struct {
	Number          int     `json:"number"`
	Title           string  `json:"title"`
	Description     string  `json:"description"`
	Author          string  `json:"author,omitempty"`
	Status          string  `json:"status"`
	Type            string  `json:"type"`
	Category        string  `json:"category,omitempty"`
	Created         string  `json:"created,omitempty"`
	URL             string  `json:"url"`
	SimilarityScore float64 `json:"similarity_score"`
}

// SearchEIPsResponse is the response for EIP search.
type SearchEIPsResponse struct {
	Type                string             `json:"type"`
	Query               string             `json:"query"`
	StatusFilter        string             `json:"status_filter,omitempty"`
	CategoryFilter      string             `json:"category_filter,omitempty"`
	TypeFilter          string             `json:"type_filter,omitempty"`
	TotalMatches        int                `json:"total_matches"`
	Results             []*SearchEIPResult `json:"results"`
	AvailableStatuses   []string           `json:"available_statuses"`
	AvailableCategories []string           `json:"available_categories"`
	AvailableTypes      []string           `json:"available_types"`
}

// Service provides search across examples, runbooks, and EIPs.
type Service struct {
	exampleIndex ExampleSearcher
	moduleReg    *module.Registry
	runbookIndex RunbookSearcher
	runbookReg   RunbookTagProvider
	eipIndex     EIPSearcher
	eipReg       EIPMetadataProvider
}

// New creates a new search service.
func New(
	exampleIndex ExampleSearcher,
	moduleReg *module.Registry,
	runbookIndex RunbookSearcher,
	runbookReg RunbookTagProvider,
	eipIndex EIPSearcher,
	eipReg EIPMetadataProvider,
) *Service {
	return &Service{
		exampleIndex: exampleIndex,
		moduleReg:    moduleReg,
		runbookIndex: runbookIndex,
		runbookReg:   runbookReg,
		eipIndex:     eipIndex,
		eipReg:       eipReg,
	}
}

// NormalizeSearchType validates and normalizes a search type string.
func NormalizeSearchType(searchType string) (string, error) {
	switch strings.TrimSpace(strings.ToLower(searchType)) {
	case SearchTypeExamples:
		return SearchTypeExamples, nil
	case SearchTypeRunbooks, SearchTypeNotebooks:
		return SearchTypeRunbooks, nil
	case SearchTypeEIPs:
		return SearchTypeEIPs, nil
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

// SearchEIPs searches EIPs with optional status, category, and type filters.
func (s *Service) SearchEIPs(
	query, statusFilter, categoryFilter, typeFilter string,
	limit int,
) (*SearchEIPsResponse, error) {
	if s.eipIndex == nil || s.eipReg == nil {
		return nil, fmt.Errorf("EIP search index not available")
	}

	limit = clampSearchLimit(limit, MaxEIPSearchLimit)

	availableStatuses := s.eipReg.Statuses()
	availableCategories := s.eipReg.Categories()
	availableTypes := s.eipReg.Types()

	if statusFilter != "" && !slices.Contains(availableStatuses, statusFilter) {
		return nil, fmt.Errorf(
			"unknown status: %q. Available statuses: %s",
			statusFilter,
			strings.Join(availableStatuses, ", "),
		)
	}

	if categoryFilter != "" && !slices.Contains(availableCategories, categoryFilter) {
		return nil, fmt.Errorf(
			"unknown category: %q. Available categories: %s",
			categoryFilter,
			strings.Join(availableCategories, ", "),
		)
	}

	if typeFilter != "" && !slices.Contains(availableTypes, typeFilter) {
		return nil, fmt.Errorf(
			"unknown type: %q. Available types: %s",
			typeFilter,
			strings.Join(availableTypes, ", "),
		)
	}

	hasFilter := statusFilter != "" || categoryFilter != "" || typeFilter != ""

	searchLimit := limit
	if hasFilter {
		searchLimit = limit * eipFilterOverscan
	}

	results, err := s.eipIndex.Search(query, searchLimit)
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}

	searchResults := make([]*SearchEIPResult, 0, len(results))

	for _, result := range results {
		if result.Score < MinEIPScore {
			continue
		}

		if statusFilter != "" && result.EIP.Status != statusFilter {
			continue
		}

		if categoryFilter != "" && result.EIP.Category != categoryFilter {
			continue
		}

		if typeFilter != "" && result.EIP.Type != typeFilter {
			continue
		}

		searchResults = append(searchResults, &SearchEIPResult{
			Number:          result.EIP.Number,
			Title:           result.EIP.Title,
			Description:     result.EIP.Description,
			Author:          result.EIP.Author,
			Status:          result.EIP.Status,
			Type:            result.EIP.Type,
			Category:        result.EIP.Category,
			Created:         result.EIP.Created,
			URL:             result.EIP.URL,
			SimilarityScore: result.Score,
		})

		if len(searchResults) >= limit {
			break
		}
	}

	return &SearchEIPsResponse{
		Type:                SearchTypeEIPs,
		Query:               query,
		StatusFilter:        statusFilter,
		CategoryFilter:      categoryFilter,
		TypeFilter:          typeFilter,
		TotalMatches:        len(searchResults),
		Results:             searchResults,
		AvailableStatuses:   availableStatuses,
		AvailableCategories: availableCategories,
		AvailableTypes:      availableTypes,
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
	_ EIPSearcher        = (*resource.EIPIndex)(nil)
)
