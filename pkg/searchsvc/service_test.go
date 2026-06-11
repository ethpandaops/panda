package searchsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/resource"
	"github.com/ethpandaops/panda/pkg/types"
)

type stubExampleSearcher struct {
	results   []resource.SearchResult
	err       error
	lastLimit int
}

func (s *stubExampleSearcher) Search(_ string, limit int) ([]resource.SearchResult, error) {
	s.lastLimit = limit

	return s.results, s.err
}

type stubRunbookSearcher struct {
	results   []resource.RunbookSearchResult
	err       error
	lastLimit int
}

func (s *stubRunbookSearcher) Search(_ string, limit int) ([]resource.RunbookSearchResult, error) {
	s.lastLimit = limit

	return s.results, s.err
}

type stubRunbookTags struct {
	tags []string
}

func (s *stubRunbookTags) Tags() []string { return append([]string(nil), s.tags...) }

type stubEIPSearcher struct {
	results   []resource.EIPSearchResult
	err       error
	lastLimit int
}

func (s *stubEIPSearcher) Search(_ string, limit int) ([]resource.EIPSearchResult, error) {
	s.lastLimit = limit

	return s.results, s.err
}

type stubEIPMetadata struct {
	statuses   []string
	categories []string
	types      []string
}

func (s *stubEIPMetadata) Statuses() []string   { return s.statuses }
func (s *stubEIPMetadata) Categories() []string { return s.categories }
func (s *stubEIPMetadata) Types() []string      { return s.types }

type stubSpecsSearcher struct {
	specs         []resource.ConsensusSpecSearchResult
	constants     []resource.ConstantSearchResult
	err           error
	lastSpecLimit int
}

func (s *stubSpecsSearcher) SearchSpecs(_ string, limit int) ([]resource.ConsensusSpecSearchResult, error) {
	s.lastSpecLimit = limit

	return s.specs, s.err
}

func (s *stubSpecsSearcher) SearchConstants(_ string, _ int) []resource.ConstantSearchResult {
	return s.constants
}

type stubSpecsMetadata struct {
	forks []string
}

func (s *stubSpecsMetadata) Forks() []string { return s.forks }

// exampleModule is a minimal module that contributes query examples.
type exampleModule struct {
	categories map[string]types.ExampleCategory
}

func (m *exampleModule) Name() string                  { return "stub-examples" }
func (m *exampleModule) Init(_ []byte) error           { return nil }
func (m *exampleModule) ApplyDefaults()                {}
func (m *exampleModule) Validate() error               { return nil }
func (m *exampleModule) Start(_ context.Context) error { return nil }
func (m *exampleModule) Stop(_ context.Context) error  { return nil }
func (m *exampleModule) Examples() map[string]types.ExampleCategory {
	return m.categories
}

func newExampleRegistry(t *testing.T, categories map[string]types.ExampleCategory) *module.Registry {
	t.Helper()

	reg := module.NewRegistry(logrus.New())
	reg.Add(&exampleModule{categories: categories})
	require.NoError(t, reg.InitModule("stub-examples", nil))

	return reg
}

func TestNormalizeSearchType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input     string
		expected  string
		expectErr bool
	}{
		{"examples", SearchTypeExamples, false},
		{"EXAMPLES", SearchTypeExamples, false},
		{"  examples  ", SearchTypeExamples, false},
		{"runbooks", SearchTypeRunbooks, false},
		{"notebooks", SearchTypeRunbooks, false},
		{"eips", SearchTypeEIPs, false},
		{"consensus-specs", SearchTypeConsensusSpecs, false},
		{"specs", SearchTypeConsensusSpecs, false},
		{"unknown", "", true},
		{"", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()

			got, err := NormalizeSearchType(tt.input)
			if tt.expectErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestClampSearchLimit(t *testing.T) {
	t.Parallel()

	const max = 10

	tests := []struct {
		name     string
		limit    int
		expected int
	}{
		{"zero uses default", 0, DefaultSearchLimit},
		{"negative clamps to 1", -5, 1},
		{"within range", 7, 7},
		{"above max clamps to max", 50, max},
		{"exactly max", max, max},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, clampSearchLimit(tt.limit, max))
		})
	}
}

func TestSearchExamplesNilIndex(t *testing.T) {
	t.Parallel()

	svc := New(nil, nil, nil, nil, nil, nil, nil, nil)

	_, err := svc.SearchExamples("query", "", "", 3)
	require.Error(t, err)
}

func TestSearchExamplesScoreAndCategoryFilter(t *testing.T) {
	t.Parallel()

	results := []resource.SearchResult{
		{CategoryKey: "blocks", CategoryName: "Blocks", Example: types.Example{Name: "high", Target: "xatu"}, Score: 0.9},
		{CategoryKey: "attestations", CategoryName: "Attestations", Example: types.Example{Name: "mid", Target: "prometheus"}, Score: 0.5},
		{CategoryKey: "blocks", CategoryName: "Blocks", Example: types.Example{Name: "below-threshold"}, Score: 0.1},
	}

	categories := map[string]types.ExampleCategory{
		"blocks":       {Name: "Blocks"},
		"attestations": {Name: "Attestations"},
	}

	t.Run("no filter drops below-threshold results", func(t *testing.T) {
		t.Parallel()

		searcher := &stubExampleSearcher{results: results}
		svc := New(searcher, newExampleRegistry(t, categories), nil, nil, nil, nil, nil, nil)

		resp, err := svc.SearchExamples("q", "", "", 5)
		require.NoError(t, err)

		assert.Len(t, resp.Results, 2)
		assert.Equal(t, "xatu", resp.Results[0].Target)
		assert.Equal(t, []string{"attestations", "blocks"}, resp.AvailableCategories)
		assert.Equal(t, 5, searcher.lastLimit, "no filter uses limit directly")

		encoded, err := json.Marshal(resp.Results[0])
		require.NoError(t, err)
		assert.Contains(t, string(encoded), `"target":"xatu"`)
		assert.NotContains(t, string(encoded), "target_cluster")
	})

	t.Run("category filter restricts results and overscans", func(t *testing.T) {
		t.Parallel()

		searcher := &stubExampleSearcher{results: results}
		svc := New(searcher, newExampleRegistry(t, categories), nil, nil, nil, nil, nil, nil)

		resp, err := svc.SearchExamples("q", "blocks", "", 4)
		require.NoError(t, err)

		require.Len(t, resp.Results, 1)
		assert.Equal(t, "blocks", resp.Results[0].CategoryKey)
		assert.Equal(t, "blocks", resp.CategoryFilter)
		assert.Equal(t, 4*exampleFilterOverscan, searcher.lastLimit)
	})

	t.Run("dataset filter restricts results and overscans", func(t *testing.T) {
		t.Parallel()

		stamped := []resource.SearchResult{
			{CategoryKey: "blocks", CategoryName: "Blocks", Example: types.Example{Name: "raw", Target: "clickhouse-raw", Dataset: "xatu-raw"}, Score: 0.9},
			{CategoryKey: "blocks", CategoryName: "Blocks", Example: types.Example{Name: "cbt", Target: "clickhouse-refined", Dataset: "xatu-cbt"}, Score: 0.8},
		}
		stampedCategories := map[string]types.ExampleCategory{
			"blocks": {Name: "Blocks", Examples: []types.Example{
				{Name: "raw", Dataset: "xatu-raw"},
				{Name: "cbt", Dataset: "xatu-cbt"},
			}},
		}

		searcher := &stubExampleSearcher{results: stamped}
		svc := New(searcher, newExampleRegistry(t, stampedCategories), nil, nil, nil, nil, nil, nil)

		resp, err := svc.SearchExamples("q", "", "xatu-cbt", 4)
		require.NoError(t, err)

		require.Len(t, resp.Results, 1)
		assert.Equal(t, "cbt", resp.Results[0].ExampleName)
		assert.Equal(t, "xatu-cbt", resp.Results[0].Dataset)
		assert.Equal(t, "xatu-cbt", resp.DatasetFilter)
		assert.Contains(t, resp.Guidance, "The target field is the datasource name the example is intended to run against.")
		assert.Contains(t, resp.Guidance, "The dataset field identifies the knowledge pack; read datasets://<dataset> for placement and required syntax before querying that dataset.")
		assert.Equal(t, 4*exampleFilterOverscan, searcher.lastLimit)
	})

	t.Run("guidance notes multiple targets", func(t *testing.T) {
		t.Parallel()

		stamped := []resource.SearchResult{
			{CategoryKey: "blocks", CategoryName: "Blocks", Example: types.Example{Name: "one", Target: "warehouse-a", Dataset: "pack-a"}, Score: 0.9},
			{CategoryKey: "blocks", CategoryName: "Blocks", Example: types.Example{Name: "two", Target: "warehouse-b", Dataset: "pack-b"}, Score: 0.8},
		}
		stampedCategories := map[string]types.ExampleCategory{
			"blocks": {Name: "Blocks", Examples: []types.Example{
				{Name: "one", Dataset: "pack-a"},
				{Name: "two", Dataset: "pack-b"},
			}},
		}

		searcher := &stubExampleSearcher{results: stamped}
		svc := New(searcher, newExampleRegistry(t, stampedCategories), nil, nil, nil, nil, nil, nil)

		resp, err := svc.SearchExamples("q", "", "", 3)
		require.NoError(t, err)

		assert.Contains(t, resp.Guidance, "When relevant examples span multiple targets, run each SQL query against its own target; combine bounded intermediate results in Python or another client-side step instead of writing one cross-datasource SQL query.")
	})

	t.Run("unknown dataset errors with available list", func(t *testing.T) {
		t.Parallel()

		stampedCategories := map[string]types.ExampleCategory{
			"blocks": {Name: "Blocks", Examples: []types.Example{{Name: "raw", Dataset: "xatu-raw"}}},
		}

		searcher := &stubExampleSearcher{}
		svc := New(searcher, newExampleRegistry(t, stampedCategories), nil, nil, nil, nil, nil, nil)

		_, err := svc.SearchExamples("q", "", "nope", 3)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown dataset")
		assert.Contains(t, err.Error(), "xatu-raw")
	})

	t.Run("unknown category errors", func(t *testing.T) {
		t.Parallel()

		searcher := &stubExampleSearcher{results: results}
		svc := New(searcher, newExampleRegistry(t, categories), nil, nil, nil, nil, nil, nil)

		_, err := svc.SearchExamples("q", "missing", "", 3)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown category")
	})

	t.Run("limit caps result count", func(t *testing.T) {
		t.Parallel()

		searcher := &stubExampleSearcher{results: results}
		svc := New(searcher, newExampleRegistry(t, categories), nil, nil, nil, nil, nil, nil)

		resp, err := svc.SearchExamples("q", "", "", 1)
		require.NoError(t, err)
		assert.Len(t, resp.Results, 1)
	})

	t.Run("search error propagates", func(t *testing.T) {
		t.Parallel()

		searcher := &stubExampleSearcher{err: fmt.Errorf("boom")}
		svc := New(searcher, newExampleRegistry(t, categories), nil, nil, nil, nil, nil, nil)

		_, err := svc.SearchExamples("q", "", "", 3)
		require.Error(t, err)
	})
}

func TestSearchRunbooks(t *testing.T) {
	t.Parallel()

	results := []resource.RunbookSearchResult{
		{Runbook: types.Runbook{Name: "finality", Tags: []string{"consensus", "finality"}}, Score: 0.9},
		{Runbook: types.Runbook{Name: "blocks", Tags: []string{"execution"}}, Score: 0.8},
		{Runbook: types.Runbook{Name: "low", Tags: []string{"consensus"}}, Score: 0.1},
	}

	tags := &stubRunbookTags{tags: []string{"execution", "consensus", "finality"}}

	t.Run("nil index errors", func(t *testing.T) {
		t.Parallel()

		svc := New(nil, nil, nil, nil, nil, nil, nil, nil)
		_, err := svc.SearchRunbooks("q", "", 3)
		require.Error(t, err)
	})

	t.Run("no filter sorts tags and drops low scores", func(t *testing.T) {
		t.Parallel()

		searcher := &stubRunbookSearcher{results: results}
		svc := New(nil, nil, searcher, tags, nil, nil, nil, nil)

		resp, err := svc.SearchRunbooks("q", "", 5)
		require.NoError(t, err)

		assert.Len(t, resp.Results, 2)
		assert.Equal(t, []string{"consensus", "execution", "finality"}, resp.AvailableTags)
		assert.Equal(t, 5, searcher.lastLimit)
	})

	t.Run("tag filter restricts and overscans", func(t *testing.T) {
		t.Parallel()

		searcher := &stubRunbookSearcher{results: results}
		svc := New(nil, nil, searcher, tags, nil, nil, nil, nil)

		resp, err := svc.SearchRunbooks("q", "finality", 3)
		require.NoError(t, err)

		require.Len(t, resp.Results, 1)
		assert.Equal(t, "finality", resp.Results[0].Name)
		assert.Equal(t, 3*runbookFilterOverscan, searcher.lastLimit)
	})

	t.Run("unknown tag errors", func(t *testing.T) {
		t.Parallel()

		searcher := &stubRunbookSearcher{results: results}
		svc := New(nil, nil, searcher, tags, nil, nil, nil, nil)

		_, err := svc.SearchRunbooks("q", "nonexistent", 3)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown tag")
	})
}

func TestSearchEIPs(t *testing.T) {
	t.Parallel()

	results := []resource.EIPSearchResult{
		{EIP: types.EIP{Number: 1, Status: "Final", Category: "Core", Type: "Standards Track"}, Score: 0.9},
		{EIP: types.EIP{Number: 2, Status: "Draft", Category: "Networking", Type: "Standards Track"}, Score: 0.8},
		{EIP: types.EIP{Number: 3, Status: "Final", Category: "Core", Type: "Meta"}, Score: 0.1},
	}

	meta := &stubEIPMetadata{
		statuses:   []string{"Final", "Draft"},
		categories: []string{"Core", "Networking"},
		types:      []string{"Standards Track", "Meta"},
	}

	t.Run("nil index errors", func(t *testing.T) {
		t.Parallel()

		svc := New(nil, nil, nil, nil, nil, nil, nil, nil)
		_, err := svc.SearchEIPs("q", "", "", "", 3)
		require.Error(t, err)
	})

	t.Run("no filter drops low scores", func(t *testing.T) {
		t.Parallel()

		searcher := &stubEIPSearcher{results: results}
		svc := New(nil, nil, nil, nil, searcher, meta, nil, nil)

		resp, err := svc.SearchEIPs("q", "", "", "", 5)
		require.NoError(t, err)

		assert.Len(t, resp.Results, 2)
		assert.Equal(t, 5, searcher.lastLimit)
	})

	t.Run("combined filters narrow results and overscan applies", func(t *testing.T) {
		t.Parallel()

		searcher := &stubEIPSearcher{results: results}
		svc := New(nil, nil, nil, nil, searcher, meta, nil, nil)

		resp, err := svc.SearchEIPs("q", "Final", "Core", "Standards Track", 3)
		require.NoError(t, err)

		require.Len(t, resp.Results, 1)
		assert.Equal(t, 1, resp.Results[0].Number)
		assert.Equal(t, 3*eipFilterOverscan, searcher.lastLimit)
	})

	t.Run("unknown filter values error", func(t *testing.T) {
		t.Parallel()

		searcher := &stubEIPSearcher{results: results}
		svc := New(nil, nil, nil, nil, searcher, meta, nil, nil)

		_, statusErr := svc.SearchEIPs("q", "Nope", "", "", 3)
		require.Error(t, statusErr)
		assert.Contains(t, statusErr.Error(), "unknown status")

		_, catErr := svc.SearchEIPs("q", "", "Nope", "", 3)
		require.Error(t, catErr)
		assert.Contains(t, catErr.Error(), "unknown category")

		_, typeErr := svc.SearchEIPs("q", "", "", "Nope", 3)
		require.Error(t, typeErr)
		assert.Contains(t, typeErr.Error(), "unknown type")
	})
}

func TestSearchSpecs(t *testing.T) {
	t.Parallel()

	specs := []resource.ConsensusSpecSearchResult{
		{Spec: types.ConsensusSpec{Fork: "deneb", Topic: "beacon-chain"}, Score: 0.9},
		{Spec: types.ConsensusSpec{Fork: "phase0", Topic: "beacon-chain"}, Score: 0.8},
		{Spec: types.ConsensusSpec{Fork: "deneb", Topic: "validator"}, Score: 0.1},
	}
	constants := []resource.ConstantSearchResult{
		{Constant: types.SpecConstant{Name: "MAX_BLOBS", Fork: "deneb"}, Score: 0.7},
		{Constant: types.SpecConstant{Name: "SLOTS_PER_EPOCH", Fork: "phase0"}, Score: 0.6},
	}

	meta := &stubSpecsMetadata{forks: []string{"phase0", "deneb"}}

	t.Run("nil index errors", func(t *testing.T) {
		t.Parallel()

		svc := New(nil, nil, nil, nil, nil, nil, nil, nil)
		_, err := svc.SearchSpecs("q", "", 3)
		require.Error(t, err)
	})

	t.Run("no filter drops low spec scores and merges constants", func(t *testing.T) {
		t.Parallel()

		searcher := &stubSpecsSearcher{specs: specs, constants: constants}
		svc := New(nil, nil, nil, nil, nil, nil, searcher, meta)

		resp, err := svc.SearchSpecs("q", "", 5)
		require.NoError(t, err)

		assert.Len(t, resp.Specs, 2)
		assert.Len(t, resp.Constants, 2)
		assert.Equal(t, 4, resp.TotalMatches)
		assert.Equal(t, 5, searcher.lastSpecLimit)
	})

	t.Run("fork filter restricts specs and constants and overscans", func(t *testing.T) {
		t.Parallel()

		searcher := &stubSpecsSearcher{specs: specs, constants: constants}
		svc := New(nil, nil, nil, nil, nil, nil, searcher, meta)

		resp, err := svc.SearchSpecs("q", "deneb", 3)
		require.NoError(t, err)

		require.Len(t, resp.Specs, 1)
		assert.Equal(t, "deneb", resp.Specs[0].Fork)
		require.Len(t, resp.Constants, 1)
		assert.Equal(t, "deneb", resp.Constants[0].Fork)
		assert.Equal(t, 3*specsFilterOverscan, searcher.lastSpecLimit)
	})

	t.Run("unknown fork errors", func(t *testing.T) {
		t.Parallel()

		searcher := &stubSpecsSearcher{specs: specs, constants: constants}
		svc := New(nil, nil, nil, nil, nil, nil, searcher, meta)

		_, err := svc.SearchSpecs("q", "nope", 3)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown fork")
	})
}

func TestSearchAll(t *testing.T) {
	t.Parallel()

	t.Run("aggregates only available indices", func(t *testing.T) {
		t.Parallel()

		exampleSearcher := &stubExampleSearcher{results: []resource.SearchResult{
			{CategoryKey: "blocks", Example: types.Example{Name: "ex"}, Score: 0.9},
		}}
		runbookSearcher := &stubRunbookSearcher{results: []resource.RunbookSearchResult{
			{
				Runbook: types.Runbook{
					Name:        "Debug",
					Description: "Debug a network.",
					Tags:        []string{"debugging"},
					Content:     "full runbook body",
				},
				Score: 0.9,
			},
		}}
		specsSearcher := &stubSpecsSearcher{specs: []resource.ConsensusSpecSearchResult{
			{Spec: types.ConsensusSpec{Fork: "deneb", Topic: "beacon-chain"}, Score: 0.9},
		}}

		svc := New(
			exampleSearcher,
			newExampleRegistry(t, map[string]types.ExampleCategory{"blocks": {Name: "Blocks"}}),
			runbookSearcher, &stubRunbookTags{tags: []string{"debugging"}},
			nil, nil,
			specsSearcher, &stubSpecsMetadata{forks: []string{"deneb"}},
		)

		resp, err := svc.SearchAll("q", 3)
		require.NoError(t, err)

		assert.Equal(t, "all", resp.Type)
		require.NotNil(t, resp.Examples)
		require.NotNil(t, resp.Runbooks)
		require.NotNil(t, resp.Specs)
		assert.Nil(t, resp.EIPs)
		require.Len(t, resp.Runbooks.Results, 1)
		assert.Empty(t, resp.Runbooks.Results[0].Content)
		require.Len(t, runbookSearcher.results, 1)
		assert.Equal(t, "full runbook body", runbookSearcher.results[0].Runbook.Content)
	})

	t.Run("empty service returns no sections", func(t *testing.T) {
		t.Parallel()

		svc := New(nil, nil, nil, nil, nil, nil, nil, nil)

		resp, err := svc.SearchAll("q", 3)
		require.NoError(t, err)

		assert.Nil(t, resp.Examples)
		assert.Nil(t, resp.Runbooks)
		assert.Nil(t, resp.EIPs)
		assert.Nil(t, resp.Specs)
	})
}
