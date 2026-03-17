package resource

import (
	"fmt"
	"sort"

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

// indexedExample holds metadata and embedding for a searchable example.
type indexedExample struct {
	CategoryKey  string
	CategoryName string
	Example      types.Example
	Vector       []float32
}

// ExampleIndex provides semantic search over query examples.
type ExampleIndex struct {
	embedder embedding.Embedder
	examples []indexedExample
}

// NewExampleIndex creates and populates a semantic search index
// from query examples using batch embedding.
func NewExampleIndex(
	log logrus.FieldLogger,
	embedder embedding.Embedder,
	categories map[string]types.ExampleCategory,
) (*ExampleIndex, error) {
	log = log.WithField("component", "example_index")

	// Collect all examples and their texts for batch embedding.
	var examples []indexedExample
	var texts []string

	for catKey, cat := range categories {
		for _, ex := range cat.Examples {
			texts = append(texts, ex.Name+". "+ex.Description)

			examples = append(examples, indexedExample{
				CategoryKey:  catKey,
				CategoryName: cat.Name,
				Example:      ex,
			})
		}
	}

	log.WithField("example_count", len(examples)).Info("Embedding examples")

	vectors, err := embedder.EmbedBatch(texts)
	if err != nil {
		return nil, fmt.Errorf("batch embedding examples: %w", err)
	}

	for i := range examples {
		examples[i].Vector = vectors[i]
	}

	log.WithField("example_count", len(examples)).Info("Example index built")

	return &ExampleIndex{
		embedder: embedder,
		examples: examples,
	}, nil
}

// Search returns the top-k semantically similar examples for a query.
func (idx *ExampleIndex) Search(query string, limit int) ([]SearchResult, error) {
	queryVec, err := idx.embedder.Embed(query)
	if err != nil {
		return nil, fmt.Errorf("embedding query: %w", err)
	}

	type scored struct {
		index int
		score float64
	}

	scores := make([]scored, 0, len(idx.examples))
	for i, ex := range idx.examples {
		scores = append(scores, scored{index: i, score: dotProduct(queryVec, ex.Vector)})
	}

	sort.Slice(scores, func(i, j int) bool {
		return scores[i].score > scores[j].score
	})

	if limit > len(scores) {
		limit = len(scores)
	}

	results := make([]SearchResult, 0, limit)
	for _, s := range scores[:limit] {
		ex := idx.examples[s.index]
		results = append(results, SearchResult{
			CategoryKey:  ex.CategoryKey,
			CategoryName: ex.CategoryName,
			Example:      ex.Example,
			Score:        s.score,
		})
	}

	return results, nil
}

// Close releases resources held by the index.
func (idx *ExampleIndex) Close() error {
	return idx.embedder.Close()
}

// dotProduct computes the dot product of two vectors.
// For L2-normalized vectors this equals cosine similarity.
func dotProduct(a, b []float32) float64 {
	var sum float64
	for i := range a {
		sum += float64(a[i]) * float64(b[i])
	}

	return sum
}
