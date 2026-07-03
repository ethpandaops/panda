package resource

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	// maxEmbedChars bounds the size of a single text chunk before embedding.
	maxEmbedChars = 600

	// textMatchBoost is added to an existing similarity score when a query
	// matches an item's text. textMatchBase is the score assigned when a text
	// match has no prior similarity score.
	textMatchBoost = 0.15
	textMatchBase  = 0.30
)

var (
	codeBlockRe = regexp.MustCompile("(?s)```.*?```")
	linkRe      = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)
	bareURLRe   = regexp.MustCompile(`https?://\S+`)
)

// dotProduct computes the dot product of two vectors.
// For L2-normalized vectors this equals cosine similarity.
// Mismatched lengths (e.g. an embedding model or dimensionality change between
// index build and query embed) score 0 instead of panicking or computing a
// meaningless prefix product.
func dotProduct(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0
	}

	var sum float64
	for i := range a {
		sum += float64(a[i]) * float64(b[i])
	}

	return sum
}

// stripForEmbedding removes code blocks, URLs, tables, and dense hex
// from text before embedding.
func stripForEmbedding(text string) string {
	text = codeBlockRe.ReplaceAllString(text, "")
	text = linkRe.ReplaceAllString(text, "$1")
	text = bareURLRe.ReplaceAllString(text, "")

	lines := strings.Split(text, "\n")
	filtered := make([]string, 0, len(lines))

	for _, line := range lines {
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

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}

	// Walk back to avoid splitting a multi-byte UTF-8 character.
	for maxLen > 0 && !utf8.RuneStart(s[maxLen]) {
		maxLen--
	}

	return s[:maxLen]
}
