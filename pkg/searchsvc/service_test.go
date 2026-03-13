package searchsvc

import (
	"errors"
	"testing"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/resource"
	"github.com/ethpandaops/panda/pkg/types"
)

type testModule struct {
	name     string
	examples map[string]types.ExampleCategory
}

func (m *testModule) Name() string                               { return m.name }
func (m *testModule) Init(_ []byte) error                        { return nil }
func (m *testModule) Validate() error                            { return nil }
func (m *testModule) Examples() map[string]types.ExampleCategory { return m.examples }

type fakeExampleSearcher struct {
	results   []resource.SearchResult
	err       error
	lastQuery string
	lastLimit int
}

func (s *fakeExampleSearcher) Search(query string, limit int) ([]resource.SearchResult, error) {
	s.lastQuery = query
	s.lastLimit = limit
	return s.results, s.err
}

type fakeRunbookSearcher struct {
	results   []resource.RunbookSearchResult
	err       error
	lastQuery string
	lastLimit int
}

func (s *fakeRunbookSearcher) Search(query string, limit int) ([]resource.RunbookSearchResult, error) {
	s.lastQuery = query
	s.lastLimit = limit
	return s.results, s.err
}

type fakeRunbookTags struct {
	tags []string
}

func (p fakeRunbookTags) Tags() []string {
	return append([]string(nil), p.tags...)
}

func TestNormalizeSearchTypeAndClamp(t *testing.T) {
	t.Parallel()

	if got, err := NormalizeSearchType(" examples "); err != nil || got != SearchTypeExamples {
		t.Fatalf("NormalizeSearchType(examples) = (%q, %v), want (%q, nil)", got, err, SearchTypeExamples)
	}

	if got, err := NormalizeSearchType("NOTEBOOKS"); err != nil || got != SearchTypeRunbooks {
		t.Fatalf("NormalizeSearchType(notebooks) = (%q, %v), want (%q, nil)", got, err, SearchTypeRunbooks)
	}

	if _, err := NormalizeSearchType("invalid"); err == nil {
		t.Fatal("NormalizeSearchType(invalid) error = nil, want error")
	}

	if got := clampSearchLimit(0, MaxExampleSearchLimit); got != DefaultSearchLimit {
		t.Fatalf("clampSearchLimit(0) = %d, want %d", got, DefaultSearchLimit)
	}

	if got := clampSearchLimit(-2, MaxExampleSearchLimit); got != 1 {
		t.Fatalf("clampSearchLimit(-2) = %d, want 1", got)
	}

	if got := clampSearchLimit(99, MaxRunbookSearchLimit); got != MaxRunbookSearchLimit {
		t.Fatalf("clampSearchLimit(99) = %d, want %d", got, MaxRunbookSearchLimit)
	}
}

func TestServiceSearchExamples(t *testing.T) {
	t.Parallel()

	moduleReg := newModuleRegistry(t, map[string]types.ExampleCategory{
		"validators": {Name: "Validators"},
		"blocks":     {Name: "Blocks"},
	})

	searcher := &fakeExampleSearcher{
		results: []resource.SearchResult{
			{
				CategoryKey:  "validators",
				CategoryName: "Validators",
				Example: types.Example{
					Name:        "Validator effectiveness",
					Description: "Track validator health",
					Query:       "SELECT 1",
					Cluster:     "xatu",
				},
				Score: 0.91,
			},
			{
				CategoryKey:  "blocks",
				CategoryName: "Blocks",
				Example: types.Example{
					Name:        "Ignored block query",
					Description: "Wrong category",
					Query:       "SELECT 2",
					Cluster:     "xatu",
				},
				Score: 0.89,
			},
			{
				CategoryKey:  "validators",
				CategoryName: "Validators",
				Example: types.Example{
					Name:        "Low score",
					Description: "Should be filtered",
					Query:       "SELECT 3",
					Cluster:     "xatu",
				},
				Score: 0.1,
			},
		},
	}

	service := New(searcher, moduleReg, nil, nil)
	resp, err := service.SearchExamples("validator", "validators", 1)
	if err != nil {
		t.Fatalf("SearchExamples() error = %v", err)
	}

	if searcher.lastQuery != "validator" || searcher.lastLimit != 3 {
		t.Fatalf("SearchExamples() search args = (%q, %d), want (validator, 3)", searcher.lastQuery, searcher.lastLimit)
	}

	if resp.Type != SearchTypeExamples || resp.TotalMatches != 1 {
		t.Fatalf("SearchExamples() = %#v, want one example response", resp)
	}

	if resp.Results[0].ExampleName != "Validator effectiveness" {
		t.Fatalf("SearchExamples() first result = %#v, want validator example", resp.Results[0])
	}

	if got := resp.AvailableCategories; len(got) != 2 || got[0] != "blocks" || got[1] != "validators" {
		t.Fatalf("AvailableCategories = %v, want sorted categories", got)
	}

	if _, err := service.SearchExamples("validator", "missing", 1); err == nil {
		t.Fatal("SearchExamples(unknown category) error = nil, want error")
	}

	searcher.err = errors.New("boom")
	if _, err := service.SearchExamples("validator", "", 2); err == nil || err.Error() != "search failed: boom" {
		t.Fatalf("SearchExamples(search error) = %v, want wrapped error", err)
	}

	if _, err := New(nil, moduleReg, nil, nil).SearchExamples("validator", "", 1); err == nil {
		t.Fatal("SearchExamples(nil index) error = nil, want error")
	}
}

func TestServiceSearchRunbooks(t *testing.T) {
	t.Parallel()

	searcher := &fakeRunbookSearcher{
		results: []resource.RunbookSearchResult{
			{
				Runbook: types.Runbook{
					Name:          "Investigate Finality Delay",
					Description:   "Find finality bottlenecks",
					Tags:          []string{"finality", "consensus"},
					Prerequisites: []string{"xatu"},
					Content:       "Runbook body",
					FilePath:      "finality.md",
				},
				Score: 0.8,
			},
			{
				Runbook: types.Runbook{
					Name:        "Wrong tag",
					Description: "Should be filtered",
					Tags:        []string{"logs"},
					Content:     "Filtered",
					FilePath:    "logs.md",
				},
				Score: 0.7,
			},
			{
				Runbook: types.Runbook{
					Name:        "Low score",
					Description: "Too weak",
					Tags:        []string{"finality"},
					Content:     "Filtered",
					FilePath:    "low.md",
				},
				Score: 0.1,
			},
		},
	}

	service := New(nil, nil, searcher, fakeRunbookTags{tags: []string{"logs", "finality"}})
	resp, err := service.SearchRunbooks("finality", "finality", 2)
	if err != nil {
		t.Fatalf("SearchRunbooks() error = %v", err)
	}

	if searcher.lastQuery != "finality" || searcher.lastLimit != 4 {
		t.Fatalf("SearchRunbooks() search args = (%q, %d), want (finality, 4)", searcher.lastQuery, searcher.lastLimit)
	}

	if resp.Type != SearchTypeRunbooks || resp.TotalMatches != 1 {
		t.Fatalf("SearchRunbooks() = %#v, want one runbook response", resp)
	}

	if resp.Results[0].Name != "Investigate Finality Delay" {
		t.Fatalf("SearchRunbooks() first result = %#v, want finality runbook", resp.Results[0])
	}

	if got := resp.AvailableTags; len(got) != 2 || got[0] != "finality" || got[1] != "logs" {
		t.Fatalf("AvailableTags = %v, want sorted tags", got)
	}

	if _, err := service.SearchRunbooks("finality", "missing", 1); err == nil {
		t.Fatal("SearchRunbooks(unknown tag) error = nil, want error")
	}

	searcher.err = errors.New("boom")
	if _, err := service.SearchRunbooks("finality", "", 1); err == nil || err.Error() != "search failed: boom" {
		t.Fatalf("SearchRunbooks(search error) = %v, want wrapped error", err)
	}

	if _, err := New(nil, nil, nil, nil).SearchRunbooks("finality", "", 1); err == nil {
		t.Fatal("SearchRunbooks(nil index) error = nil, want error")
	}
}

func newModuleRegistry(t *testing.T, examples map[string]types.ExampleCategory) *module.Registry {
	t.Helper()

	reg := module.NewRegistry(logrus.New())
	reg.Add(&testModule{name: "examples", examples: examples})

	if err := reg.InitModule("examples", nil); err != nil {
		t.Fatalf("InitModule(examples) error = %v", err)
	}

	return reg
}
