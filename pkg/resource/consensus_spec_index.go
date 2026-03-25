package resource

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/embedding"
	"github.com/ethpandaops/panda/pkg/types"
)

const (
	exactConstantScore     = 1.0
	prefixConstantScore    = 0.8
	substringConstantScore = 0.5
	specTextMatchBoost     = 0.15
	specTextMatchBase      = 0.30
)

// ConsensusSpecSearchResult includes a spec and its similarity score.
type ConsensusSpecSearchResult struct {
	Spec  types.ConsensusSpec `json:"spec"`
	Score float64             `json:"similarity_score"`
}

// ConstantSearchResult includes a constant and its similarity score.
type ConstantSearchResult struct {
	Constant types.SpecConstant `json:"constant"`
	Score    float64            `json:"similarity_score"`
}

// indexedSpecChunk holds a reference to a spec and its embedding vector.
type indexedSpecChunk struct {
	SpecIdx int
	Vector  []float32
}

// specSearchText holds pre-lowercased text for a spec to avoid repeated
// strings.ToLower calls on potentially large content during search.
type specSearchText struct {
	title   string
	topic   string
	content string
}

// ConsensusSpecIndex provides semantic search over consensus specs with
// hybrid scoring and exact constant name matching.
type ConsensusSpecIndex struct {
	embedder   embedding.Embedder
	chunks     []indexedSpecChunk
	specs      []types.ConsensusSpec
	searchText []specSearchText
	constants  []types.SpecConstant
}

// NewConsensusSpecIndex creates a semantic search index from consensus specs.
func NewConsensusSpecIndex(
	log logrus.FieldLogger,
	embedder embedding.Embedder,
	specs []types.ConsensusSpec,
	constants []types.SpecConstant,
) (*ConsensusSpecIndex, error) {
	log = log.WithField("component", "consensus_spec_index")

	specIndices := make([]int, 0, len(specs)*2)
	texts := make([]string, 0, len(specs)*2)

	for specIdx, spec := range specs {
		for _, chunk := range chunkSpec(spec) {
			specIndices = append(specIndices, specIdx)
			texts = append(texts, chunk)
		}
	}

	log.WithFields(logrus.Fields{
		"spec_count":     len(specs),
		"constant_count": len(constants),
		"total_chunks":   len(texts),
	}).Info("Embedding consensus spec chunks")

	vectors, err := embedder.EmbedBatch(texts)
	if err != nil {
		return nil, fmt.Errorf("batch embedding spec chunks: %w", err)
	}

	chunks := make([]indexedSpecChunk, len(specIndices))
	for i, specIdx := range specIndices {
		chunks[i] = indexedSpecChunk{
			SpecIdx: specIdx,
			Vector:  vectors[i],
		}
	}

	// Pre-compute lowercased text for text-match boosting so we don't call
	// strings.ToLower on potentially large spec content on every search query.
	searchText := make([]specSearchText, len(specs))
	for i, spec := range specs {
		searchText[i] = specSearchText{
			title:   strings.ToLower(spec.Title),
			topic:   strings.ToLower(spec.Topic),
			content: strings.ToLower(spec.Content),
		}
	}

	log.WithFields(logrus.Fields{
		"specs":  len(specs),
		"chunks": len(chunks),
	}).Info("Consensus spec index built")

	return &ConsensusSpecIndex{
		embedder:   embedder,
		chunks:     chunks,
		specs:      specs,
		searchText: searchText,
		constants:  constants,
	}, nil
}

// SearchSpecs returns the top-k semantically similar specs for a query.
func (idx *ConsensusSpecIndex) SearchSpecs(
	query string,
	limit int,
) ([]ConsensusSpecSearchResult, error) {
	queryVec, err := idx.embedder.Embed(query)
	if err != nil {
		return nil, fmt.Errorf("embedding query: %w", err)
	}

	type scored struct {
		specIdx int
		score   float64
	}

	chunkScores := make([]scored, 0, len(idx.chunks))
	for _, chunk := range idx.chunks {
		chunkScores = append(chunkScores, scored{
			specIdx: chunk.SpecIdx,
			score:   dotProduct(queryVec, chunk.Vector),
		})
	}

	// Deduplicate: keep best score per spec.
	bestBySpec := make(map[int]float64, len(idx.specs))
	for _, s := range chunkScores {
		if s.score > bestBySpec[s.specIdx] {
			bestBySpec[s.specIdx] = s.score
		}
	}

	// Text match boost for queries > 4 chars using pre-lowercased text.
	if len(query) > 4 {
		lowerQuery := strings.ToLower(query)

		for specIdx, st := range idx.searchText {
			if strings.Contains(st.title, lowerQuery) ||
				strings.Contains(st.topic, lowerQuery) ||
				strings.Contains(st.content, lowerQuery) {
				if existing, ok := bestBySpec[specIdx]; ok {
					bestBySpec[specIdx] = existing + specTextMatchBoost
				} else {
					bestBySpec[specIdx] = specTextMatchBase
				}
			}
		}
	}

	results := make([]scored, 0, len(bestBySpec))
	for specIdx, score := range bestBySpec {
		results = append(results, scored{specIdx: specIdx, score: score})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})

	if limit > len(results) {
		limit = len(results)
	}

	out := make([]ConsensusSpecSearchResult, 0, limit)
	for _, s := range results[:limit] {
		out = append(out, ConsensusSpecSearchResult{
			Spec:  idx.specs[s.specIdx],
			Score: s.score,
		})
	}

	return out, nil
}

// SearchConstants returns constants matching the query by exact name match,
// prefix match, or substring match.
func (idx *ConsensusSpecIndex) SearchConstants(
	query string,
	limit int,
) []ConstantSearchResult {
	upperQuery := strings.ToUpper(strings.TrimSpace(query))
	if upperQuery == "" {
		return nil
	}

	type scored struct {
		constant types.SpecConstant
		score    float64
	}

	var results []scored

	for _, c := range idx.constants {
		upperName := strings.ToUpper(c.Name)

		var score float64

		switch {
		case upperName == upperQuery:
			score = exactConstantScore
		case strings.HasPrefix(upperName, upperQuery):
			score = prefixConstantScore
		case strings.Contains(upperName, upperQuery):
			score = substringConstantScore
		default:
			continue
		}

		results = append(results, scored{constant: c, score: score})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})

	if limit > len(results) {
		limit = len(results)
	}

	out := make([]ConstantSearchResult, 0, limit)
	for _, s := range results[:limit] {
		out = append(out, ConstantSearchResult{
			Constant: s.constant,
			Score:    s.score,
		})
	}

	return out
}

// chunkSpec splits a consensus spec into chunks suitable for embedding.
func chunkSpec(spec types.ConsensusSpec) []string {
	body := stripForEmbedding(spec.Content)
	prefix := spec.Title + " (" + spec.Fork + "/" + spec.Topic + "). "
	fullText := prefix + body

	if len(fullText) <= maxEmbedChars {
		return []string{fullText}
	}

	paragraphs := strings.Split(body, "\n\n")

	var chunks []string

	current := prefix

	for _, para := range paragraphs {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}

		if len(current)+len(para)+1 <= maxEmbedChars {
			current += "\n" + para
		} else {
			if current != "" {
				chunks = append(chunks, truncate(current, maxEmbedChars))
			}

			current = prefix + para
		}
	}

	if current != "" {
		chunks = append(chunks, truncate(current, maxEmbedChars))
	}

	if len(chunks) == 0 {
		chunks = []string{truncate(fullText, maxEmbedChars)}
	}

	return chunks
}
