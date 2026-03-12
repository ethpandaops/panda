package tool

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/resource"
	"github.com/ethpandaops/panda/pkg/searchsvc"
	"github.com/ethpandaops/panda/pkg/types"
)

type testExamplesModule struct {
	examples map[string]types.ExampleCategory
}

func (m *testExamplesModule) Name() string                               { return "examples" }
func (m *testExamplesModule) Init(_ []byte) error                        { return nil }
func (m *testExamplesModule) Validate() error                            { return nil }
func (m *testExamplesModule) Examples() map[string]types.ExampleCategory { return m.examples }

type fakeExampleSearcher struct {
	results []resource.SearchResult
}

func (s fakeExampleSearcher) Search(string, int) ([]resource.SearchResult, error) {
	return s.results, nil
}

type fakeRunbookSearcher struct {
	results []resource.RunbookSearchResult
}

func (s fakeRunbookSearcher) Search(string, int) ([]resource.RunbookSearchResult, error) {
	return s.results, nil
}

type fakeRunbookRegistry struct {
	tags []string
}

func (r fakeRunbookRegistry) Tags() []string {
	return append([]string(nil), r.tags...)
}

func TestNewSearchToolMetadata(t *testing.T) {
	t.Parallel()

	def := NewSearchTool(logrus.New(), nil, nil, nil, nil)
	if def.Tool.Name != SearchToolName {
		t.Fatalf("tool name = %q, want %q", def.Tool.Name, SearchToolName)
	}

	if def.Tool.Description != searchDescription {
		t.Fatalf("tool description = %q, want searchDescription", def.Tool.Description)
	}

	if len(def.Tool.InputSchema.Required) != 2 {
		t.Fatalf("required fields = %v, want type and query", def.Tool.InputSchema.Required)
	}
}

func TestSearchHandlerHandle(t *testing.T) {
	t.Parallel()

	handler := &searchHandler{
		service: searchsvc.New(
			fakeExampleSearcher{results: []resource.SearchResult{{
				CategoryKey:  "validators",
				CategoryName: "Validators",
				Example: types.Example{
					Name:        "Validator effectiveness",
					Description: "Track health",
					Query:       "SELECT 1",
					Cluster:     "xatu",
				},
				Score: 0.9,
			}}},
			newToolModuleRegistry(t),
			fakeRunbookSearcher{results: []resource.RunbookSearchResult{{
				Runbook: types.Runbook{
					Name:        "Investigate Finality Delay",
					Description: "Finality checklist",
					Tags:        []string{"finality"},
					Content:     "Runbook body",
					FilePath:    "finality.md",
				},
				Score: 0.8,
			}}},
			fakeRunbookRegistry{tags: []string{"finality", "logs"}},
		),
	}

	result, err := handler.handle(context.Background(), searchToolRequest(map[string]any{
		"type":  "invalid",
		"query": "validator",
	}))
	if err != nil {
		t.Fatalf("handle(invalid type) error = %v", err)
	}
	if !result.IsError {
		t.Fatal("handle(invalid type) IsError = false, want true")
	}

	result, err = handler.handle(context.Background(), searchToolRequest(map[string]any{
		"type": "examples",
	}))
	if err != nil {
		t.Fatalf("handle(empty query) error = %v", err)
	}
	if !result.IsError {
		t.Fatal("handle(empty query) IsError = false, want true")
	}

	result, err = handler.handle(context.Background(), searchToolRequest(map[string]any{
		"type":  "examples",
		"query": "validator",
		"tag":   "finality",
	}))
	if err != nil {
		t.Fatalf("handle(examples with tag) error = %v", err)
	}
	if !result.IsError {
		t.Fatal("handle(examples with tag) IsError = false, want true")
	}

	result, err = handler.handle(context.Background(), searchToolRequest(map[string]any{
		"type":     "runbooks",
		"query":    "finality",
		"category": "validators",
	}))
	if err != nil {
		t.Fatalf("handle(runbooks with category) error = %v", err)
	}
	if !result.IsError {
		t.Fatal("handle(runbooks with category) IsError = false, want true")
	}

	result, err = handler.handle(context.Background(), searchToolRequest(map[string]any{
		"type":     "examples",
		"query":    "validator",
		"category": "validators",
		"limit":    2,
	}))
	if err != nil {
		t.Fatalf("handle(examples) error = %v", err)
	}
	if result.IsError {
		t.Fatalf("handle(examples) result = %#v, want success", result)
	}

	var examplesResp searchsvc.SearchExamplesResponse
	if err := json.Unmarshal([]byte(resultText(t, result)), &examplesResp); err != nil {
		t.Fatalf("json.Unmarshal(examples result) error = %v", err)
	}
	if examplesResp.TotalMatches != 1 || examplesResp.Results[0].ExampleName != "Validator effectiveness" {
		t.Fatalf("examples response = %#v, want validator result", examplesResp)
	}

	result, err = handler.handle(context.Background(), searchToolRequest(map[string]any{
		"type":  "notebooks",
		"query": "finality",
		"tag":   "finality",
	}))
	if err != nil {
		t.Fatalf("handle(runbooks) error = %v", err)
	}
	if result.IsError {
		t.Fatalf("handle(runbooks) result = %#v, want success", result)
	}

	var runbooksResp searchsvc.SearchRunbooksResponse
	if err := json.Unmarshal([]byte(resultText(t, result)), &runbooksResp); err != nil {
		t.Fatalf("json.Unmarshal(runbooks result) error = %v", err)
	}
	if runbooksResp.TotalMatches != 1 || runbooksResp.Results[0].Name != "Investigate Finality Delay" {
		t.Fatalf("runbooks response = %#v, want finality result", runbooksResp)
	}
}

func searchToolRequest(args map[string]any) mcp.CallToolRequest {
	return mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      SearchToolName,
			Arguments: args,
		},
	}
}

func newToolModuleRegistry(t *testing.T) *module.Registry {
	t.Helper()

	reg := module.NewRegistry(logrus.New())
	reg.Add(&testExamplesModule{
		examples: map[string]types.ExampleCategory{
			"validators": {Name: "Validators"},
			"blocks":     {Name: "Blocks"},
		},
	})

	if err := reg.InitModule("examples", nil); err != nil {
		t.Fatalf("InitModule(examples) error = %v", err)
	}

	return reg
}
