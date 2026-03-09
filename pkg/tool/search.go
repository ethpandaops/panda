package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/mcp/pkg/extension"
	"github.com/ethpandaops/mcp/pkg/resource"
	"github.com/ethpandaops/mcp/pkg/searchsvc"
	"github.com/ethpandaops/mcp/runbooks"
)

const SearchToolName = "search"

const searchDescription = `Search indexed examples and runbooks using semantic search.

Use ` + "`type=\"examples\"`" + ` for query snippets (SQL, PromQL, LogQL) and ` + "`type=\"runbooks\"`" + ` for multi-step investigation procedures.

` + "`type=\"notebooks\"`" + ` is accepted as an alias for runbooks.

Examples:
- search(type="examples", query="block")
- search(type="examples", query="validator", category="validators")
- search(type="runbooks", query="network not finalizing")
- search(type="runbooks", query="slow clickhouse query", tag="performance")`

type searchHandler struct {
	log     logrus.FieldLogger
	service *searchsvc.Service
}

func NewSearchTool(
	log logrus.FieldLogger,
	exampleIndex *resource.ExampleIndex,
	extensionReg *extension.Registry,
	runbookIndex *resource.RunbookIndex,
	runbookReg *runbooks.Registry,
) Definition {
	h := &searchHandler{
		log:     log.WithField("tool", SearchToolName),
		service: searchsvc.New(exampleIndex, extensionReg, runbookIndex, runbookReg),
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
						"enum": []string{
							searchsvc.SearchTypeExamples,
							searchsvc.SearchTypeRunbooks,
							searchsvc.SearchTypeNotebooks,
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
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum results to return. Defaults to 3. Max is 10 for examples and 5 for runbooks.",
						"minimum":     1,
						"maximum":     searchsvc.MaxExampleSearchLimit,
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

	searchType, err := searchsvc.NormalizeSearchType(request.GetString("type", ""))
	if err != nil {
		return CallToolError(err), nil
	}

	query := request.GetString("query", "")
	if query == "" {
		return CallToolError(fmt.Errorf("query is required and cannot be empty")), nil
	}

	switch searchType {
	case searchsvc.SearchTypeExamples:
		return h.searchExamples(request, query)
	case searchsvc.SearchTypeRunbooks:
		return h.searchRunbooks(request, query)
	default:
		return CallToolError(fmt.Errorf("unsupported search type: %q", searchType)), nil
	}
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
