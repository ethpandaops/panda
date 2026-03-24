package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/searchsvc"
)

const SearchToolName = "search"

const searchDescription = `Search indexed examples, runbooks, EIPs, and consensus specs using semantic search.

When ` + "`type`" + ` is omitted, searches across all types and returns combined results. Use a specific type to narrow results.

` + "`type=\"examples\"`" + ` for query snippets (SQL, PromQL, LogQL), ` + "`type=\"runbooks\"`" + ` for multi-step investigation procedures, ` + "`type=\"eips\"`" + ` for Ethereum Improvement Proposals, ` + "`type=\"consensus-specs\"`" + ` for consensus-specs documents and protocol constants. ` + "`type=\"notebooks\"`" + ` is accepted as an alias for runbooks, ` + "`type=\"specs\"`" + ` as an alias for consensus-specs.

Examples:
- search(query="blob propagation getBlobs")
- search(query="validator performance")
- search(type="examples", query="block", category="validators")
- search(type="runbooks", query="network not finalizing", tag="finality")
- search(type="eips", query="account abstraction", status="Final")
- search(type="consensus-specs", query="MAX_EFFECTIVE_BALANCE")
- search(type="consensus-specs", query="fork choice", fork="deneb")`

type searchHandler struct {
	log     logrus.FieldLogger
	service *searchsvc.Service
}

// NewSearchTool creates the unified search MCP tool definition.
func NewSearchTool(
	log logrus.FieldLogger,
	service *searchsvc.Service,
) Definition {
	h := &searchHandler{
		log:     log.WithField("tool", SearchToolName),
		service: service,
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
						"description": "Optional. When omitted, searches all types. Use 'examples' for query snippets, 'runbooks' for investigation procedures, 'eips' for Ethereum Improvement Proposals, or 'consensus-specs' for consensus-specs documents and protocol constants. 'notebooks' is an alias for 'runbooks', 'specs' is an alias for 'consensus-specs'.",
						"enum": []string{
							searchsvc.SearchTypeExamples,
							searchsvc.SearchTypeRunbooks,
							searchsvc.SearchTypeNotebooks,
							searchsvc.SearchTypeEIPs,
							searchsvc.SearchTypeConsensusSpecs,
							searchsvc.SearchTypeSpecsAlias,
						},
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
					"status": map[string]any{
						"type":        "string",
						"description": "Optional for type='eips': filter by EIP status (e.g., 'Final', 'Draft', 'Review')",
					},
					"fork": map[string]any{
						"type":        "string",
						"description": "Optional for type='consensus-specs': filter by consensus layer fork (e.g., 'phase0', 'deneb', 'electra')",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum results to return. Defaults to 3. Max is 10 for examples/eips/specs and 5 for runbooks.",
						"minimum":     1,
						"maximum":     searchsvc.MaxExampleSearchLimit,
					},
				},
				Required: []string{"query"},
			},
		},
		Handler: h.handle,
	}
}

func (h *searchHandler) handle(
	_ context.Context,
	request mcp.CallToolRequest,
) (*mcp.CallToolResult, error) {
	h.log.Debug("Handling search request")

	query := request.GetString("query", "")
	if query == "" {
		return CallToolError(fmt.Errorf("query is required and cannot be empty")), nil
	}

	rawType := request.GetString("type", "")
	if rawType == "" {
		return h.searchAll(request, query)
	}

	searchType, err := searchsvc.NormalizeSearchType(rawType)
	if err != nil {
		return CallToolError(err), nil
	}

	switch searchType {
	case searchsvc.SearchTypeExamples:
		return h.searchExamples(request, query)
	case searchsvc.SearchTypeRunbooks:
		return h.searchRunbooks(request, query)
	case searchsvc.SearchTypeEIPs:
		return h.searchEIPs(request, query)
	case searchsvc.SearchTypeConsensusSpecs:
		return h.searchSpecs(request, query)
	default:
		return CallToolError(fmt.Errorf("unsupported search type: %q", searchType)), nil
	}
}

func (h *searchHandler) searchAll(
	request mcp.CallToolRequest,
	query string,
) (*mcp.CallToolResult, error) {
	response, err := h.service.SearchAll(
		query,
		request.GetInt("limit", searchsvc.DefaultSearchLimit),
	)
	if err != nil {
		return CallToolError(err), nil
	}

	data, err := json.MarshalIndent(response, "", "  ")
	if err != nil {
		return CallToolError(fmt.Errorf("marshaling response: %w", err)), nil
	}

	h.log.WithFields(logrus.Fields{
		"type":  "all",
		"query": query,
	}).Debug("Search completed")

	return CallToolSuccess(string(data)), nil
}

func (h *searchHandler) searchExamples(
	request mcp.CallToolRequest,
	query string,
) (*mcp.CallToolResult, error) {
	if tag := request.GetString("tag", ""); tag != "" {
		return CallToolError(fmt.Errorf("tag is only supported for type=%q", searchsvc.SearchTypeRunbooks)), nil
	}

	response, err := h.service.SearchExamples(
		query,
		request.GetString("category", ""),
		request.GetInt("limit", searchsvc.DefaultSearchLimit),
	)
	if err != nil {
		return CallToolError(err), nil
	}

	data, err := json.MarshalIndent(response, "", "  ")
	if err != nil {
		return CallToolError(fmt.Errorf("marshaling response: %w", err)), nil
	}

	h.log.WithFields(logrus.Fields{
		"type":    searchsvc.SearchTypeExamples,
		"query":   query,
		"matches": response.TotalMatches,
	}).Debug("Search completed")

	return CallToolSuccess(string(data)), nil
}

func (h *searchHandler) searchRunbooks(
	request mcp.CallToolRequest,
	query string,
) (*mcp.CallToolResult, error) {
	if category := request.GetString("category", ""); category != "" {
		return CallToolError(fmt.Errorf("category is only supported for type=%q", searchsvc.SearchTypeExamples)), nil
	}

	response, err := h.service.SearchRunbooks(
		query,
		request.GetString("tag", ""),
		request.GetInt("limit", searchsvc.DefaultSearchLimit),
	)
	if err != nil {
		return CallToolError(err), nil
	}

	data, err := json.MarshalIndent(response, "", "  ")
	if err != nil {
		return CallToolError(fmt.Errorf("marshaling response: %w", err)), nil
	}

	h.log.WithFields(logrus.Fields{
		"type":    searchsvc.SearchTypeRunbooks,
		"query":   query,
		"matches": response.TotalMatches,
	}).Debug("Search completed")

	return CallToolSuccess(string(data)), nil
}

func (h *searchHandler) searchEIPs(
	request mcp.CallToolRequest,
	query string,
) (*mcp.CallToolResult, error) {
	if tag := request.GetString("tag", ""); tag != "" {
		return CallToolError(fmt.Errorf("tag is only supported for type=%q", searchsvc.SearchTypeRunbooks)), nil
	}

	if category := request.GetString("category", ""); category != "" {
		return CallToolError(fmt.Errorf("category is only supported for type=%q", searchsvc.SearchTypeExamples)), nil
	}

	response, err := h.service.SearchEIPs(
		query,
		request.GetString("status", ""),
		"",
		"",
		request.GetInt("limit", searchsvc.DefaultSearchLimit),
	)
	if err != nil {
		return CallToolError(err), nil
	}

	data, err := json.MarshalIndent(response, "", "  ")
	if err != nil {
		return CallToolError(fmt.Errorf("marshaling response: %w", err)), nil
	}

	h.log.WithFields(logrus.Fields{
		"type":    searchsvc.SearchTypeEIPs,
		"query":   query,
		"matches": response.TotalMatches,
	}).Debug("Search completed")

	return CallToolSuccess(string(data)), nil
}

func (h *searchHandler) searchSpecs(
	request mcp.CallToolRequest,
	query string,
) (*mcp.CallToolResult, error) {
	if tag := request.GetString("tag", ""); tag != "" {
		return CallToolError(fmt.Errorf("tag is only supported for type=%q", searchsvc.SearchTypeRunbooks)), nil
	}

	if category := request.GetString("category", ""); category != "" {
		return CallToolError(fmt.Errorf("category is only supported for type=%q", searchsvc.SearchTypeExamples)), nil
	}

	response, err := h.service.SearchSpecs(
		query,
		request.GetString("fork", ""),
		request.GetInt("limit", searchsvc.DefaultSearchLimit),
	)
	if err != nil {
		return CallToolError(err), nil
	}

	data, err := json.MarshalIndent(response, "", "  ")
	if err != nil {
		return CallToolError(fmt.Errorf("marshaling response: %w", err)), nil
	}

	h.log.WithFields(logrus.Fields{
		"type":    searchsvc.SearchTypeConsensusSpecs,
		"query":   query,
		"matches": response.TotalMatches,
	}).Debug("Search completed")

	return CallToolSuccess(string(data)), nil
}
