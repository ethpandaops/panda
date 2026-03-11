package resource

import (
	"fmt"
	"strings"

	"github.com/kelindar/search"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/embedding"
	"github.com/ethpandaops/panda/pkg/types"
)

// RunbookSearchResult includes the runbook and its similarity score.
type RunbookSearchResult struct {
	Runbook types.Runbook `json:"runbook"`
	Score   float64       `json:"similarity_score"`
}

// indexedRunbook holds metadata for a searchable runbook.
type indexedRunbook struct {
	Runbook types.Runbook
}

// RunbookIndex provides semantic search over runbooks.
type RunbookIndex struct {
	embedder *embedding.Embedder
	index    *search.Index[int]
	runbooks []indexedRunbook
}

// NewRunbookIndex creates and populates a semantic search index from runbooks.
func NewRunbookIndex(
	log logrus.FieldLogger,
	embedder *embedding.Embedder,
	runbooks []types.Runbook,
) (*RunbookIndex, error) {
	log = log.WithField("component", "runbook_index")

	index := search.NewIndex[int]()
	indexed := make([]indexedRunbook, 0, len(runbooks))

	for i, rb := range runbooks {
		text := buildRunbookSearchText(rb)

		vec, err := embedder.Embed(text)
		if err != nil {
			return nil, fmt.Errorf("embedding runbook %q: %w", rb.Name, err)
		}

		index.Add(vec, i)

		indexed = append(indexed, indexedRunbook{
			Runbook: rb,
		})
	}

	log.WithField("runbook_count", len(indexed)).Info("Runbook index built")

	return &RunbookIndex{
		embedder: embedder,
		index:    index,
		runbooks: indexed,
	}, nil
}

// Search returns the top-k semantically similar runbooks for a query.
func (idx *RunbookIndex) Search(query string, limit int) ([]RunbookSearchResult, error) {
	queryVec, err := idx.embedder.Embed(query)
	if err != nil {
		return nil, fmt.Errorf("embedding query: %w", err)
	}

	matches := idx.index.Search(queryVec, limit)

	results := make([]RunbookSearchResult, 0, len(matches))
	for _, match := range matches {
		rb := idx.runbooks[match.Value]
		results = append(results, RunbookSearchResult{
			Runbook: rb.Runbook,
			Score:   match.Relevance,
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
