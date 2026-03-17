package resource

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/embedding"
	"github.com/ethpandaops/panda/pkg/types"
)

// RunbookSearchResult includes the runbook and its similarity score.
type RunbookSearchResult struct {
	Runbook types.Runbook `json:"runbook"`
	Score   float64       `json:"similarity_score"`
}

// indexedRunbook holds metadata and embedding for a searchable runbook.
type indexedRunbook struct {
	Runbook types.Runbook
	Vector  []float32
}

// RunbookIndex provides semantic search over runbooks.
type RunbookIndex struct {
	embedder embedding.Embedder
	runbooks []indexedRunbook
}

// NewRunbookIndex creates and populates a semantic search index from runbooks
// using batch embedding.
func NewRunbookIndex(
	log logrus.FieldLogger,
	embedder embedding.Embedder,
	runbooks []types.Runbook,
) (*RunbookIndex, error) {
	log = log.WithField("component", "runbook_index")

	texts := make([]string, len(runbooks))
	for i, rb := range runbooks {
		texts[i] = buildRunbookSearchText(rb)
	}

	vectors, err := embedder.EmbedBatch(texts)
	if err != nil {
		return nil, fmt.Errorf("batch embedding runbooks: %w", err)
	}

	indexed := make([]indexedRunbook, len(runbooks))
	for i, rb := range runbooks {
		indexed[i] = indexedRunbook{Runbook: rb, Vector: vectors[i]}
	}

	log.WithField("runbook_count", len(indexed)).Info("Runbook index built")

	return &RunbookIndex{
		embedder: embedder,
		runbooks: indexed,
	}, nil
}

// Search returns the top-k semantically similar runbooks for a query.
func (idx *RunbookIndex) Search(query string, limit int) ([]RunbookSearchResult, error) {
	queryVec, err := idx.embedder.Embed(query)
	if err != nil {
		return nil, fmt.Errorf("embedding query: %w", err)
	}

	type scored struct {
		index int
		score float64
	}

	scores := make([]scored, 0, len(idx.runbooks))
	for i, rb := range idx.runbooks {
		scores = append(scores, scored{index: i, score: dotProduct(queryVec, rb.Vector)})
	}

	sort.Slice(scores, func(i, j int) bool {
		return scores[i].score > scores[j].score
	})

	if limit > len(scores) {
		limit = len(scores)
	}

	results := make([]RunbookSearchResult, 0, limit)
	for _, s := range scores[:limit] {
		rb := idx.runbooks[s.index]
		results = append(results, RunbookSearchResult{
			Runbook: rb.Runbook,
			Score:   s.score,
		})
	}

	return results, nil
}

// buildRunbookSearchText creates the text to embed for semantic search.
// Indexes name, description, tags, and overview (first paragraph before code).
func buildRunbookSearchText(rb types.Runbook) string {
	overview := extractOverview(rb.Content, 300)

	parts := []string{
		rb.Name,
		rb.Description,
		strings.Join(rb.Tags, " "),
		overview,
	}

	return strings.Join(parts, ". ")
}

// extractOverview extracts the content before the first code block or ## header.
// This captures the intent/context without including embedded code snippets.
func extractOverview(content string, maxLen int) string {
	lines := strings.Split(content, "\n")
	var overview strings.Builder

	for _, line := range lines {
		// Stop at code blocks or section headers
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "## ") {
			break
		}

		overview.WriteString(line + " ")

		if overview.Len() > maxLen {
			break
		}
	}

	return strings.TrimSpace(overview.String())
}
