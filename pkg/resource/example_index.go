package resource

import (
	"fmt"

	"github.com/kelindar/search"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/embedding"
	"github.com/ethpandaops/panda/pkg/types"
)

// SearchResult includes the example and its similarity score.
type SearchResult struct {
	CategoryKey  string        `json:"category_key"`
	CategoryName string        `json:"category_name"`
	Example      types.Example `json:"example"`
	Score        float64       `json:"similarity_score"`
}

// indexedExample holds metadata for a searchable example.
type indexedExample struct {
	CategoryKey  string
	CategoryName string
	Example      types.Example
}

// ExampleIndex provides semantic search over query examples.
type ExampleIndex struct {
	embedder *embedding.Embedder
	index    *search.Index[int]
	examples []indexedExample
}

// NewExampleIndex creates and populates a semantic search index
// from query examples.
func NewExampleIndex(
	log logrus.FieldLogger,
	embedder *embedding.Embedder,
	categories map[string]types.ExampleCategory,
) (*ExampleIndex, error) {
	log = log.WithField("component", "example_index")

	index := search.NewIndex[int]()
	var examples []indexedExample

	i := 0

	for catKey, cat := range categories {
		for _, ex := range cat.Examples {
			text := ex.Name + ". " + ex.Description

			vec, err := embedder.Embed(text)
			if err != nil {
				return nil, fmt.Errorf("embedding example %q: %w", ex.Name, err)
			}

			index.Add(vec, i)

			examples = append(examples, indexedExample{
				CategoryKey:  catKey,
				CategoryName: cat.Name,
				Example:      ex,
			})
			i++
		}
	}

	log.WithField("example_count", len(examples)).Info("Example index built")

	return &ExampleIndex{
		embedder: embedder,
		index:    index,
		examples: examples,
	}, nil
}

// Search returns the top-k semantically similar examples for a query.
func (idx *ExampleIndex) Search(query string, limit int) ([]SearchResult, error) {
	queryVec, err := idx.embedder.Embed(query)
	if err != nil {
		return nil, fmt.Errorf("embedding query: %w", err)
	}

	matches := idx.index.Search(queryVec, limit)

	results := make([]SearchResult, 0, len(matches))
	for _, match := range matches {
		ex := idx.examples[match.Value]
		results = append(results, SearchResult{
			CategoryKey:  ex.CategoryKey,
			CategoryName: ex.CategoryName,
			Example:      ex.Example,
			Score:        match.Relevance,
		})
	}

	return results, nil
}

// Close releases resources held by the index.
func (idx *ExampleIndex) Close() error {
	return idx.embedder.Close()
}
