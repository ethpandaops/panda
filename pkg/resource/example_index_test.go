package resource

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/ethpandaops/panda/pkg/types"
	"github.com/sirupsen/logrus"
)

type stubEmbedder struct {
	vectors map[string][]float32
	err     error
}

func (s stubEmbedder) Embed(text string) ([]float32, error) {
	if s.err != nil {
		return nil, s.err
	}

	vector, ok := s.vectors[text]
	if !ok {
		return nil, errors.New("missing vector")
	}

	return vector, nil
}

func TestNewExampleIndexAndSearch(t *testing.T) {
	t.Parallel()

	embedder := stubEmbedder{
		vectors: map[string][]float32{
			"Head blocks. Recent head block query":         {1, 0},
			"Missed attestations. Validator issue example": {0, 1},
			"head blocks": {1, 0},
		},
	}

	idx, err := NewExampleIndex(logrus.New(), embedder, map[string]types.ExampleCategory{
		"queries": {
			Name: "Queries",
			Examples: []types.Example{
				{Name: "Head blocks", Description: "Recent head block query"},
				{Name: "Missed attestations", Description: "Validator issue example"},
			},
		},
	})
	if err != nil {
		t.Fatalf("NewExampleIndex() error = %v", err)
	}

	results, err := idx.Search("head blocks", 1)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Search() len = %d, want 1", len(results))
	}
	if got := results[0].Example.Name; got != "Head blocks" {
		t.Fatalf("Search() top result = %q, want Head blocks", got)
	}
	if got := results[0].CategoryName; got != "Queries" {
		t.Fatalf("Search() category name = %q, want Queries", got)
	}
}

func TestExampleIndexValidatesEmbedderAndLimit(t *testing.T) {
	t.Parallel()

	if _, err := NewExampleIndex(logrus.New(), nil, nil); err == nil || err.Error() != "embedder is required" {
		t.Fatalf("NewExampleIndex(nil) error = %v, want embedder is required", err)
	}

	idx := &ExampleIndex{
		embedder: stubEmbedder{
			vectors: map[string][]float32{
				"query": {1},
			},
		},
	}

	results, err := idx.Search("query", 0)
	if err != nil {
		t.Fatalf("Search(limit=0) error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("Search(limit=0) len = %d, want 0", len(results))
	}

	if _, err := (*ExampleIndex)(nil).Search("query", 1); err == nil || err.Error() != "example index is not initialized" {
		t.Fatalf("nil Search() error = %v, want example index is not initialized", err)
	}
}

func TestDotProductHandlesEmptyAndSignedVectors(t *testing.T) {
	t.Parallel()

	if got := dotProduct(nil, nil); got != 0 {
		t.Fatalf("dotProduct(nil, nil) = %v, want 0", got)
	}

	if got := dotProduct([]float32{1.5, -2, 3}, []float32{4, 5, -6}); got != -22 {
		t.Fatalf("dotProduct(signed vectors) = %v, want -22", got)
	}
}

func TestExampleIndexCloseClearsState(t *testing.T) {
	t.Parallel()

	idx := &ExampleIndex{
		embedder: stubEmbedder{vectors: map[string][]float32{"query": {1}}},
		examples: []indexedExample{
			{
				CategoryKey:  "queries",
				CategoryName: "Queries",
				Example:      types.Example{Name: "Head blocks"},
				Vector:       []float32{1, 0},
			},
		},
	}

	if err := idx.Close(); err != nil {
		t.Fatalf("Close() error = %v, want nil", err)
	}

	if idx.embedder != nil {
		t.Fatalf("embedder = %#v, want nil", idx.embedder)
	}

	if idx.examples != nil {
		t.Fatalf("examples = %#v, want nil", idx.examples)
	}
}

func TestSearchResultJSONTags(t *testing.T) {
	t.Parallel()

	payload, err := json.Marshal(SearchResult{
		CategoryKey:  "queries",
		CategoryName: "Queries",
		Example:      types.Example{Name: "Head blocks"},
		Score:        0.98,
	})
	if err != nil {
		t.Fatalf("json.Marshal(SearchResult) error = %v", err)
	}

	got := string(payload)
	for _, needle := range []string{`"category_key":"queries"`, `"category_name":"Queries"`, `"similarity_score":0.98`} {
		if !containsJSONField(got, needle) {
			t.Fatalf("json = %s, want field %s", got, needle)
		}
	}
}

func containsJSONField(payload, field string) bool {
	return len(payload) >= len(field) && (payload == field || contains(payload, field))
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}

	return false
}
