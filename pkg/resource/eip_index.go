package resource

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/embedding"
	"github.com/ethpandaops/panda/pkg/types"
)

const (
	maxEmbedChars  = 600
	textMatchBoost = 0.15
	textMatchBase  = 0.30
)

// EIPSearchResult includes the EIP and its similarity score.
type EIPSearchResult struct {
	EIP   types.EIP `json:"eip"`
	Score float64   `json:"similarity_score"`
}

// indexedEIPChunk holds a reference to an EIP and its embedding vector.
type indexedEIPChunk struct {
	EIPIdx int
	Vector []float32
}

// EIPIndex provides semantic search over EIPs with hybrid scoring.
type EIPIndex struct {
	embedder *embedding.Embedder
	chunks   []indexedEIPChunk
	eips     []types.EIP
}

// NewEIPIndex creates a semantic search index from EIPs.
// It reuses cached vectors where the text hash matches, embedding only changed chunks.
// Returns the index and the updated vector map (for cache persistence).
func NewEIPIndex(
	log logrus.FieldLogger,
	embedder *embedding.Embedder,
	eips []types.EIP,
	cachedVectors map[string]types.EIPVector,
) (*EIPIndex, map[string]types.EIPVector, error) {
	log = log.WithField("component", "eip_index")

	updatedVectors := make(map[string]types.EIPVector, len(eips)*2)
	chunks := make([]indexedEIPChunk, 0, len(eips)*2)

	var embedded, reused int

	for eipIdx, eip := range eips {
		eipChunks := chunkEIP(eip)

		for chunkIdx, chunk := range eipChunks {
			chunkKey := fmt.Sprintf("%d:%d", eip.Number, chunkIdx)
			textHash := sha256Hex(chunk)

			var vec []float32

			if cached, ok := cachedVectors[chunkKey]; ok && cached.TextHash == textHash {
				vec = cached.Vector
				reused++
			} else {
				var err error

				vec, err = embedder.Embed(chunk)
				if err != nil {
					return nil, nil, fmt.Errorf("embedding EIP-%d chunk %d: %w", eip.Number, chunkIdx, err)
				}

				embedded++
			}

			updatedVectors[chunkKey] = types.EIPVector{
				TextHash: textHash,
				Vector:   vec,
			}

			chunks = append(chunks, indexedEIPChunk{
				EIPIdx: eipIdx,
				Vector: vec,
			})
		}
	}

	log.WithFields(logrus.Fields{
		"eip_count": len(eips),
		"chunks":    len(chunks),
		"embedded":  embedded,
		"reused":    reused,
	}).Info("EIP index built")

	return &EIPIndex{
		embedder: embedder,
		chunks:   chunks,
		eips:     eips,
	}, updatedVectors, nil
}

// Search returns the top-k semantically similar EIPs for a query.
// Uses hybrid scoring: vector similarity + exact text match boost.
func (idx *EIPIndex) Search(query string, limit int) ([]EIPSearchResult, error) {
	queryVec, err := idx.embedder.Embed(query)
	if err != nil {
		return nil, fmt.Errorf("embedding query: %w", err)
	}

	// Score all chunks via dot product.
	type scored struct {
		eipIdx int
		score  float64
	}

	chunkScores := make([]scored, 0, len(idx.chunks))
	for _, chunk := range idx.chunks {
		chunkScores = append(chunkScores, scored{
			eipIdx: chunk.EIPIdx,
			score:  dotProduct(queryVec, chunk.Vector),
		})
	}

	// Deduplicate: keep best score per EIP.
	bestByEIP := make(map[int]float64, len(idx.eips))
	for _, s := range chunkScores {
		if s.score > bestByEIP[s.eipIdx] {
			bestByEIP[s.eipIdx] = s.score
		}
	}

	// Hybrid boost: add text match score for queries > 4 chars.
	if len(query) > 4 {
		lowerQuery := strings.ToLower(query)

		for eipIdx, eip := range idx.eips {
			if containsText(eip, lowerQuery) {
				if existing, ok := bestByEIP[eipIdx]; ok {
					bestByEIP[eipIdx] = existing + textMatchBoost
				} else {
					bestByEIP[eipIdx] = textMatchBase
				}
			}
		}
	}

	// Sort by score descending.
	results := make([]scored, 0, len(bestByEIP))
	for eipIdx, score := range bestByEIP {
		results = append(results, scored{eipIdx: eipIdx, score: score})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})

	if limit > len(results) {
		limit = len(results)
	}

	out := make([]EIPSearchResult, 0, limit)
	for _, s := range results[:limit] {
		out = append(out, EIPSearchResult{
			EIP:   idx.eips[s.eipIdx],
			Score: s.score,
		})
	}

	return out, nil
}

// chunkEIP splits an EIP into chunks suitable for embedding.
func chunkEIP(eip types.EIP) []string {
	body := stripForEmbedding(eip.Content)
	fullText := eip.Title + ". " + eip.Description + "\n" + body

	if len(fullText) <= maxEmbedChars {
		return []string{fullText}
	}

	prefix := eip.Title + ". "
	paragraphs := strings.Split(body, "\n\n")

	var chunks []string

	current := prefix + eip.Description

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

var (
	codeBlockRe = regexp.MustCompile("(?s)```.*?```")
	linkRe      = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)
	bareURLRe   = regexp.MustCompile(`https?://\S+`)
	tableSepRe  = regexp.MustCompile(`^\s*\|[-|:\s]+\|\s*$`)
)

// stripForEmbedding removes code blocks, URLs, tables, and dense hex
// from text before embedding.
func stripForEmbedding(text string) string {
	text = codeBlockRe.ReplaceAllString(text, "")
	text = linkRe.ReplaceAllString(text, "$1")
	text = bareURLRe.ReplaceAllString(text, "")

	lines := strings.Split(text, "\n")
	filtered := make([]string, 0, len(lines))

	for _, line := range lines {
		if tableSepRe.MatchString(line) {
			continue
		}

		if strings.HasPrefix(strings.TrimSpace(line), "|") {
			continue
		}

		if len(line) > 80 && !strings.Contains(line, " ") {
			continue
		}

		filtered = append(filtered, line)
	}

	return strings.Join(filtered, "\n")
}

func containsText(eip types.EIP, lowerQuery string) bool {
	return strings.Contains(strings.ToLower(eip.Title), lowerQuery) ||
		strings.Contains(strings.ToLower(eip.Description), lowerQuery) ||
		strings.Contains(strings.ToLower(eip.Content), lowerQuery)
}

func sha256Hex(text string) string {
	h := sha256.Sum256([]byte(text))
	return fmt.Sprintf("%x", h)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}

	return s[:maxLen]
}
