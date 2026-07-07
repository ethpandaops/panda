package resource

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/embedding"
	"github.com/ethpandaops/panda/pkg/textmatch"
	"github.com/ethpandaops/panda/pkg/types"
)

const (
	// sectionTextMaxLen caps how much of a section body is embedded per child
	// vector — enough prose for section-level retrieval without noise.
	sectionTextMaxLen = 600
	// lexicalBoostWeight scales the lexical score added on top of the dense
	// similarity. Additive so pure-semantic matches keep their scale (and the
	// downstream minimum-score filter its meaning) while exact identifiers
	// (table names, error strings) get lifted.
	lexicalBoostWeight = 0.3
	// sectionVectorWeight discounts section child vectors relative to the
	// metadata vector. Sections exist to make mid-document facts findable, but
	// the authored routing surface (name/description/triggers) must win a
	// near-tie against an incidental body match — authors control routing
	// through frontmatter, not through which section happens to embed closest.
	sectionVectorWeight = 0.95
)

// RunbookSearchResult includes the runbook and its similarity score.
type RunbookSearchResult struct {
	Runbook types.Runbook `json:"runbook"`
	Score   float64       `json:"similarity_score"`
}

// indexedRunbook holds the searchable representation of one runbook: a
// metadata vector and one full-weight vector per trigger (the authored routing
// surface), plus discounted per-section child vectors (all resolving to the
// whole runbook), and the fields used for lexical scoring.
type indexedRunbook struct {
	Runbook types.Runbook
	// routingVectors carry the authored retrieval surface: the metadata blob
	// (document space) plus each trigger embedded individually in QUERY space —
	// a trigger is a hypothetical caller query, and embedding it alone, on the
	// query side of the asymmetric model, puts it in the same space as incoming
	// queries so a near-verbatim match scores near-perfectly.
	routingVectors [][]float32
	// sectionVectors make mid-document facts findable; they score at
	// sectionVectorWeight so they never out-rank the authored surface.
	sectionVectors [][]float32
	lexFields      []string
}

// RunbookIndex provides hybrid (dense + lexical) semantic search over runbooks.
type RunbookIndex struct {
	log      logrus.FieldLogger
	embedder embedding.Embedder
	runbooks []indexedRunbook
	// mismatchWarn dedupes the dimensionality-mismatch warning: the condition
	// holds for every query until the runtime re-indexes, and one line per
	// index instance says everything a repeat would.
	mismatchWarn sync.Once
}

// NewRunbookIndex creates and populates a search index from runbooks using
// batch embedding. Each runbook gets a metadata vector, one vector per trigger,
// and per-section child vectors, so facts buried mid-document are retrievable
// while search still returns the whole runbook.
func NewRunbookIndex(
	log logrus.FieldLogger,
	embedder embedding.Embedder,
	runbooks []types.Runbook,
) (*RunbookIndex, error) {
	log = log.WithField("component", "runbook_index")

	// Document-space texts: the metadata blob plus sections, per runbook.
	// Query-space texts: every trigger, embedded on the query side so incoming
	// queries match them within one space.
	docTexts := make([]string, 0, len(runbooks)*6)
	queryTexts := make([]string, 0, len(runbooks)*4)
	sectionCounts := make([]int, len(runbooks))
	triggerCounts := make([]int, len(runbooks))

	for i, rb := range runbooks {
		sections := buildSectionTexts(rb)
		sectionCounts[i] = len(sections)
		triggerCounts[i] = len(rb.Triggers)
		docTexts = append(docTexts, buildRunbookSearchText(rb))
		docTexts = append(docTexts, sections...)
		queryTexts = append(queryTexts, rb.Triggers...)
	}

	docVectors, err := embedder.EmbedBatch(docTexts)
	if err != nil {
		return nil, fmt.Errorf("batch embedding runbook documents: %w", err)
	}

	triggerVectors, err := embedder.EmbedQueryBatch(queryTexts)
	if err != nil {
		return nil, fmt.Errorf("batch embedding runbook triggers: %w", err)
	}

	indexed := make([]indexedRunbook, len(runbooks))

	docOffset, trigOffset := 0, 0

	for i, rb := range runbooks {
		routing := make([][]float32, 0, 1+triggerCounts[i])
		routing = append(routing, docVectors[docOffset])
		routing = append(routing, triggerVectors[trigOffset:trigOffset+triggerCounts[i]]...)

		indexed[i] = indexedRunbook{
			Runbook:        rb,
			routingVectors: routing,
			sectionVectors: docVectors[docOffset+1 : docOffset+1+sectionCounts[i]],
			lexFields:      buildLexicalFields(rb),
		}
		docOffset += 1 + sectionCounts[i]
		trigOffset += triggerCounts[i]
	}

	log.WithFields(logrus.Fields{
		"runbook_count": len(indexed),
		"vector_count":  len(docVectors) + len(triggerVectors),
	}).Info("Runbook index built")

	return &RunbookIndex{
		log:      log,
		embedder: embedder,
		runbooks: indexed,
	}, nil
}

// Search returns the top-k runbooks for a query, scored by the best dense
// similarity across the runbook's vectors plus a lexical boost for exact
// token/identifier matches.
func (idx *RunbookIndex) Search(query string, limit int) ([]RunbookSearchResult, error) {
	queryVec, err := idx.embedder.Embed(query)
	if err != nil {
		return nil, fmt.Errorf("embedding query: %w", err)
	}

	lexQuery := textmatch.NewQuery(query)

	// A query vector whose length differs from the index's vectors means the
	// embedding space changed under us (model/dims cutover mid-window). Dense
	// scores all become 0 and ranking degrades to lexical-only until the
	// runtime's identity check re-indexes — surviving, but worth shouting about.
	if len(idx.runbooks) > 0 && len(idx.runbooks[0].routingVectors) > 0 &&
		len(idx.runbooks[0].routingVectors[0]) != len(queryVec) {
		idx.mismatchWarn.Do(func() {
			idx.log.WithFields(logrus.Fields{
				"index_dims": len(idx.runbooks[0].routingVectors[0]),
				"query_dims": len(queryVec),
			}).Warn("Runbook search query vector does not match index dimensionality; serving lexical-only ranking until re-index")
		})
	}

	type scored struct {
		index int
		score float64
	}

	scores := make([]scored, 0, len(idx.runbooks))

	for i, rb := range idx.runbooks {
		dense := 0.0

		for _, vec := range rb.routingVectors {
			if d := dotProduct(queryVec, vec); d > dense {
				dense = d
			}
		}

		for _, vec := range rb.sectionVectors {
			if d := dotProduct(queryVec, vec) * sectionVectorWeight; d > dense {
				dense = d
			}
		}

		// Unclamped: capping at 1.0 would collapse strong candidates into a
		// tie and hand the ordering to the tie-break. The floor filter
		// downstream only needs scores to be comparable, not bounded.
		score := dense + lexicalBoostWeight*lexQuery.Score(rb.lexFields...)

		scores = append(scores, scored{index: i, score: score})
	}

	// Deterministic order: score, then file path — equal scores (including
	// ones clamped at 1.0) must not flap between runs or golden-query checks.
	sort.SliceStable(scores, func(i, j int) bool {
		if scores[i].score != scores[j].score {
			return scores[i].score > scores[j].score
		}

		return idx.runbooks[scores[i].index].Runbook.FilePath < idx.runbooks[scores[j].index].Runbook.FilePath
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

// buildRunbookSearchText creates the metadata text to embed for semantic
// search: name, description, tags, triggers (example caller queries), overview
// (first paragraph before code), and section headers.
func buildRunbookSearchText(rb types.Runbook) string {
	parts := make([]string, 0, 6)
	parts = append(parts, rb.Name, rb.Description, strings.Join(rb.Tags, " "))

	if len(rb.Triggers) > 0 {
		parts = append(parts, strings.Join(rb.Triggers, ". "))
	}

	parts = append(parts, extractOverview(rb.Content, 300))

	if headers := extractHeaders(rb.Content); headers != "" {
		parts = append(parts, headers)
	}

	return strings.Join(parts, ". ")
}

// buildSectionTexts returns one embeddable text per `##` section: the runbook
// name for context (chunks embedded in isolation lose it), the header, and the
// section's prose up to sectionTextMaxLen. Code fences are skipped. Sections
// with no prose are dropped.
func buildSectionTexts(rb types.Runbook) []string {
	texts := make([]string, 0, 8)

	var (
		header  string
		body    strings.Builder
		inFence bool
	)

	flush := func() {
		prose := strings.TrimSpace(body.String())
		if len(prose) > sectionTextMaxLen {
			// The line loop can overshoot the cap by one line; trim to the cap
			// without splitting a multi-byte rune.
			prose = strings.ToValidUTF8(prose[:sectionTextMaxLen], "")
		}

		if header != "" && prose != "" {
			texts = append(texts, rb.Name+" — "+header+". "+prose)
		}

		body.Reset()
	}

	for line := range strings.SplitSeq(rb.Content, "\n") {
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}

		if inFence {
			continue
		}

		if strings.HasPrefix(line, "## ") {
			flush()

			header = strings.TrimPrefix(line, "## ")

			continue
		}

		if header != "" && body.Len() < sectionTextMaxLen {
			body.WriteString(strings.TrimSpace(line))
			body.WriteString(" ")
		}
	}

	flush()

	return texts
}

// buildLexicalFields returns the fields lexical scoring runs against: the ref
// stem, name, description, tags, and triggers.
func buildLexicalFields(rb types.Runbook) []string {
	fields := make([]string, 0, 5)
	fields = append(fields,
		strings.TrimSuffix(rb.FilePath, ".md"),
		rb.Name,
		rb.Description,
		strings.Join(rb.Tags, " "),
	)

	if len(rb.Triggers) > 0 {
		fields = append(fields, strings.Join(rb.Triggers, ". "))
	}

	return fields
}

// extractHeaders returns the text of all markdown section headers, so section
// topics (e.g. "Finality-stall triage") match queries without embedding body prose.
func extractHeaders(content string) string {
	headers := make([]string, 0, 16)
	inFence := false

	for line := range strings.SplitSeq(content, "\n") {
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}

		if !inFence && (strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "### ")) {
			headers = append(headers, strings.TrimLeft(line, "# "))
		}
	}

	return strings.Join(headers, ". ")
}

// extractOverview extracts the content before the first code block or ## header.
// This captures the intent/context without including embedded code snippets.
func extractOverview(content string, maxLen int) string {
	var overview strings.Builder

	for line := range strings.SplitSeq(content, "\n") {
		// Stop at code blocks or section headers
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "## ") {
			break
		}

		overview.WriteString(line)
		overview.WriteString(" ")

		if overview.Len() > maxLen {
			break
		}
	}

	return strings.TrimSpace(overview.String())
}
