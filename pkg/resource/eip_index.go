package resource

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/kelindar/search"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/mcp/pkg/embedding"
	"github.com/ethpandaops/mcp/pkg/types"
)

// maxEmbedChars is the approximate character limit per embedding chunk.
// MiniLM-L6-v2 has a 512-token context window with WordPiece tokenizer.
// Markdown with numbers, URLs, and special chars tokenizes at ~1.5 chars/token
// in the worst case, so we use a conservative limit to avoid exceeding 512 tokens.
// The llama.cpp library aborts (SIGABRT) on token overflow rather than
// returning an error, so we must stay safely under the limit.
const maxEmbedChars = 600

// EIPSearchResult includes the EIP and its similarity score.
type EIPSearchResult struct {
	EIP   types.EIP `json:"eip"`
	Score float64   `json:"similarity_score"`
}

// indexEntry maps a vector index position back to an EIP.
type indexEntry struct {
	eipIdx int
}

// EIPIndex provides semantic search over Ethereum Improvement Proposals.
type EIPIndex struct {
	embedder *embedding.Embedder
	index    *search.Index[int]
	entries  []indexEntry
	eips     []types.EIP
}

// NewEIPIndex creates and populates a semantic search index from EIPs.
// Long EIPs are split into chunks, each embedded separately.
// cachedVectors provides previously computed vectors keyed by chunk key;
// only chunks whose text has changed will be re-embedded.
func NewEIPIndex(
	log logrus.FieldLogger,
	embedder *embedding.Embedder,
	eips []types.EIP,
	cachedVectors map[string]types.EIPVector,
) (*EIPIndex, map[string]types.EIPVector, error) {
	log = log.WithField("component", "eip_index")

	if cachedVectors == nil {
		cachedVectors = make(map[string]types.EIPVector)
	}

	index := search.NewIndex[int]()
	stored := make([]types.EIP, len(eips))
	copy(stored, eips)

	var entries []indexEntry

	updatedVectors := make(map[string]types.EIPVector, len(eips))

	var embedded, reused int

	for eipIdx, eip := range stored {
		chunks := chunkEIP(eip)

		for chunkIdx, chunk := range chunks {
			key := chunkKey(eip.Number, chunkIdx)
			hash := textHash(chunk)
			vecIdx := len(entries)

			if cached, ok := cachedVectors[key]; ok && cached.TextHash == hash && len(cached.Vector) > 0 {
				index.Add(cached.Vector, vecIdx)
				updatedVectors[key] = cached
				entries = append(entries, indexEntry{eipIdx: eipIdx})
				reused++

				continue
			}

			vec, err := embedder.Embed(chunk)
			if err != nil {
				return nil, nil, fmt.Errorf("embedding EIP-%d chunk %d: %w", eip.Number, chunkIdx, err)
			}

			index.Add(vec, vecIdx)
			updatedVectors[key] = types.EIPVector{
				TextHash: hash,
				Vector:   vec,
			}

			entries = append(entries, indexEntry{eipIdx: eipIdx})
			embedded++
		}
	}

	log.WithFields(logrus.Fields{
		"eip_count": len(stored),
		"chunks":    len(entries),
		"embedded":  embedded,
		"reused":    reused,
	}).Info("EIP index built")

	return &EIPIndex{
		embedder: embedder,
		index:    index,
		entries:  entries,
		eips:     stored,
	}, updatedVectors, nil
}

const (
	// textMatchBoost is the score bonus for EIPs whose full text contains the query.
	textMatchBoost = 0.15
	// textMatchBase is the base score for EIPs found only by text match (not vector search).
	textMatchBase = 0.30
)

// Search returns the top-k semantically similar EIPs for a query.
// Multiple chunks from the same EIP are deduplicated, keeping the best score.
// For queries longer than 4 characters, a hybrid approach boosts EIPs that
// contain an exact text match of the query in their title, description, or body.
func (idx *EIPIndex) Search(query string, limit int) ([]EIPSearchResult, error) {
	queryVec, err := idx.embedder.Embed(query)
	if err != nil {
		return nil, fmt.Errorf("embedding query: %w", err)
	}

	// Fetch extra matches to account for deduplication and re-ranking.
	matches := idx.index.Search(queryVec, limit*5)

	// Deduplicate chunks from the same EIP, keeping the best vector score.
	bestScore := make(map[int]float64)
	for _, match := range matches {
		entry := idx.entries[match.Value]
		if match.Relevance > bestScore[entry.eipIdx] {
			bestScore[entry.eipIdx] = match.Relevance
		}
	}

	// For queries > 4 chars, scan all EIPs for exact text matches that the
	// vector search may have missed, and boost EIPs that contain the query.
	queryLower := strings.ToLower(strings.TrimSpace(query))
	useTextBoost := len(queryLower) > 4

	if useTextBoost {
		for eipIdx := range idx.eips {
			if eipContainsText(idx.eips[eipIdx], queryLower) {
				if _, ok := bestScore[eipIdx]; !ok {
					// Not in vector results — add with a base score.
					bestScore[eipIdx] = textMatchBase
				}
				bestScore[eipIdx] += textMatchBoost
			}
		}
	}

	type scored struct {
		eipIdx int
		score  float64
	}

	candidates := make([]scored, 0, len(bestScore))
	for eipIdx, score := range bestScore {
		candidates = append(candidates, scored{eipIdx: eipIdx, score: score})
	}

	// Sort by score descending.
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})

	results := make([]EIPSearchResult, 0, limit)
	for _, c := range candidates {
		results = append(results, EIPSearchResult{
			EIP:   idx.eips[c.eipIdx],
			Score: c.score,
		})
		if len(results) >= limit {
			break
		}
	}

	return results, nil
}

// eipContainsText checks if the query appears in the EIP's title, description, or body.
func eipContainsText(eip types.EIP, queryLower string) bool {
	return strings.Contains(strings.ToLower(eip.Title), queryLower) ||
		strings.Contains(strings.ToLower(eip.Description), queryLower) ||
		strings.Contains(strings.ToLower(eip.Content), queryLower)
}

// chunkEIP splits an EIP into embedding-sized text chunks.
func chunkEIP(eip types.EIP) []string {
	// Build full text: title + description + body content.
	var full strings.Builder

	full.WriteString(eip.Title)

	if eip.Description != "" {
		full.WriteString(". ")
		full.WriteString(eip.Description)
	}

	if eip.Content != "" {
		full.WriteString("\n")
		full.WriteString(stripForEmbedding(eip.Content))
	}

	text := full.String()

	if len(text) <= maxEmbedChars {
		return []string{text}
	}

	// Split into chunks at paragraph boundaries.
	var chunks []string

	// First chunk always includes title + description as prefix.
	prefix := eip.Title
	if eip.Description != "" {
		prefix += ". " + eip.Description
	}

	remaining := text[len(prefix):]
	current := prefix

	for _, para := range strings.Split(remaining, "\n\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}

		if len(current)+len(para)+2 > maxEmbedChars {
			if current != "" {
				chunks = append(chunks, strings.TrimSpace(current))
			}
			// New chunk starts with title for context.
			current = eip.Title + ". " + para
		} else {
			current += "\n\n" + para
		}
	}

	if strings.TrimSpace(current) != "" {
		chunks = append(chunks, strings.TrimSpace(current))
	}

	if len(chunks) == 0 {
		return []string{text[:min(len(text), maxEmbedChars)]}
	}

	// Truncate any oversized chunks (e.g., single paragraphs exceeding the limit).
	for i, c := range chunks {
		if len(c) > maxEmbedChars {
			chunks[i] = c[:maxEmbedChars]
		}
	}

	return chunks
}

var (
	// codeBlockRe matches fenced code blocks (```...```).
	codeBlockRe = regexp.MustCompile("(?s)```[^\n]*\n.*?```")
	// markdownLinkRe matches [text](url) and replaces with just text.
	markdownLinkRe = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)
	// urlRe matches bare URLs.
	urlRe = regexp.MustCompile(`https?://\S+`)
	// tableRowRe matches markdown table rows (lines with pipes).
	tableRowRe = regexp.MustCompile(`^\s*\|.*\|\s*$`)
	// tableSepRe matches markdown table separators (---|---).
	tableSepRe = regexp.MustCompile(`^\s*[\|\-:\s]+$`)
)

// stripForEmbedding removes code blocks, URLs, tables, and long hex/base64
// lines that tokenize very densely but add little semantic value for search.
func stripForEmbedding(text string) string {
	text = codeBlockRe.ReplaceAllString(text, "")
	text = markdownLinkRe.ReplaceAllString(text, "$1")
	text = urlRe.ReplaceAllString(text, "")

	var lines []string
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		// Skip long single-token lines (hex, base64).
		if len(trimmed) > 80 && !strings.Contains(trimmed, " ") {
			continue
		}
		// Skip table rows and separators.
		if tableRowRe.MatchString(line) || tableSepRe.MatchString(line) {
			continue
		}

		lines = append(lines, line)
	}

	return strings.Join(lines, "\n")
}

func chunkKey(eipNumber, chunkIdx int) string {
	return fmt.Sprintf("%d:%d", eipNumber, chunkIdx)
}

func textHash(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])
}
