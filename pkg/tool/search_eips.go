package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/mcp/pkg/eips"
	"github.com/ethpandaops/mcp/pkg/resource"
)

const (
	SearchEIPsToolName    = "search_eips"
	DefaultEIPSearchLimit = 5
	MaxEIPSearchLimit     = 10
	MinEIPSimilarityScore = 0.25
)

const searchEIPsDescription = `Search Ethereum Improvement Proposals (EIPs) using semantic search.

Returns matching EIPs with metadata and links to full content.

Examples:
- search_eips(query="account abstraction") → EIPs about account abstraction
- search_eips(query="gas pricing", category="Core") → Core EIPs about gas
- search_eips(query="token standard", status="Final") → Finalized token EIPs`

// SearchEIPResult represents a single EIP search result.
type SearchEIPResult struct {
	Number          int     `json:"number"`
	Title           string  `json:"title"`
	Description     string  `json:"description"`
	Author          string  `json:"author"`
	Status          string  `json:"status"`
	Type            string  `json:"type"`
	Category        string  `json:"category,omitempty"`
	Created         string  `json:"created,omitempty"`
	URL             string  `json:"url"`
	SimilarityScore float64 `json:"similarity_score"`
}

// SearchEIPsResponse is the response from the search_eips tool.
type SearchEIPsResponse struct {
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

type searchEIPsHandler struct {
	log      logrus.FieldLogger
	index    *resource.EIPIndex
	registry *eips.Registry
}

// NewSearchEIPsTool creates the search_eips MCP tool.
func NewSearchEIPsTool(
	log logrus.FieldLogger,
	index *resource.EIPIndex,
	registry *eips.Registry,
) Definition {
	h := &searchEIPsHandler{
		log:      log.WithField("tool", SearchEIPsToolName),
		index:    index,
		registry: registry,
	}

	return Definition{
		Tool: mcp.Tool{
			Name:        SearchEIPsToolName,
			Description: searchEIPsDescription,
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Search term or phrase to find semantically similar EIPs",
					},
					"status": map[string]any{
						"type":        "string",
						"description": "Optional: filter by EIP status (e.g., 'Draft', 'Final', 'Living')",
					},
					"category": map[string]any{
						"type":        "string",
						"description": "Optional: filter by category (e.g., 'Core', 'ERC', 'Networking', 'Interface')",
					},
					"type": map[string]any{
						"type":        "string",
						"description": "Optional: filter by type (e.g., 'Standards Track', 'Meta', 'Informational')",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum results to return (default: 5, max: 10)",
						"minimum":     1,
						"maximum":     MaxEIPSearchLimit,
					},
				},
				Required: []string{"query"},
			},
		},
		Handler: h.handle,
	}
}

func (h *searchEIPsHandler) handle(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	h.log.Debug("Handling search_eips request")

	query := request.GetString("query", "")
	if query == "" {
		return CallToolError(fmt.Errorf("query is required and cannot be empty")), nil
	}

	statusFilter := request.GetString("status", "")
	categoryFilter := request.GetString("category", "")
	typeFilter := request.GetString("type", "")

	limit := request.GetInt("limit", DefaultEIPSearchLimit)
	if limit <= 0 {
		limit = DefaultEIPSearchLimit
	}

	if limit > MaxEIPSearchLimit {
		limit = MaxEIPSearchLimit
	}

	availableStatuses := h.registry.Statuses()
	availableCategories := h.registry.Categories()
	availableTypes := h.registry.Types()

	if statusFilter != "" && !slices.Contains(availableStatuses, statusFilter) {
		return CallToolError(fmt.Errorf(
			"unknown status: %q. Available: %s",
			statusFilter,
			strings.Join(availableStatuses, ", "),
		)), nil
	}

	if categoryFilter != "" && !slices.Contains(availableCategories, categoryFilter) {
		return CallToolError(fmt.Errorf(
			"unknown category: %q. Available: %s",
			categoryFilter,
			strings.Join(availableCategories, ", "),
		)), nil
	}

	if typeFilter != "" && !slices.Contains(availableTypes, typeFilter) {
		return CallToolError(fmt.Errorf(
			"unknown type: %q. Available: %s",
			typeFilter,
			strings.Join(availableTypes, ", "),
		)), nil
	}

	// Fetch extra to account for filtering.
	fetchLimit := limit
	if statusFilter != "" || categoryFilter != "" || typeFilter != "" {
		fetchLimit = limit * 3
	}

	results, err := h.index.Search(query, fetchLimit)
	if err != nil {
		return CallToolError(fmt.Errorf("search failed: %w", err)), nil
	}

	// Apply filters.
	var filtered []resource.EIPSearchResult

	for _, r := range results {
		if statusFilter != "" && r.EIP.Status != statusFilter {
			continue
		}

		if categoryFilter != "" && r.EIP.Category != categoryFilter {
			continue
		}

		if typeFilter != "" && r.EIP.Type != typeFilter {
			continue
		}

		filtered = append(filtered, r)
	}

	if len(filtered) > limit {
		filtered = filtered[:limit]
	}

	// Build response.
	searchResults := make([]*SearchEIPResult, 0, len(filtered))

	for _, r := range filtered {
		if r.Score < MinEIPSimilarityScore {
			continue
		}

		searchResults = append(searchResults, &SearchEIPResult{
			Number:          r.EIP.Number,
			Title:           r.EIP.Title,
			Description:     r.EIP.Description,
			Author:          r.EIP.Author,
			Status:          r.EIP.Status,
			Type:            r.EIP.Type,
			Category:        r.EIP.Category,
			Created:         r.EIP.Created,
			URL:             r.EIP.URL,
			SimilarityScore: r.Score,
		})
	}

	response := &SearchEIPsResponse{
		Query:               query,
		StatusFilter:        statusFilter,
		CategoryFilter:      categoryFilter,
		TypeFilter:          typeFilter,
		TotalMatches:        len(searchResults),
		Results:             searchResults,
		AvailableStatuses:   availableStatuses,
		AvailableCategories: availableCategories,
		AvailableTypes:      availableTypes,
	}

	data, err := json.MarshalIndent(response, "", "  ")
	if err != nil {
		return CallToolError(fmt.Errorf("marshaling response: %w", err)), nil
	}

	h.log.WithFields(logrus.Fields{
		"query":   query,
		"matches": len(searchResults),
	}).Debug("EIP search completed")

	return CallToolSuccess(string(data)), nil
}
