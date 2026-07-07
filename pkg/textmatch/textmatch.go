// Package textmatch provides lexical text similarity scoring — token-set
// overlap blended with character-trigram similarity — used to rank candidates
// against a query without embeddings. It complements dense semantic search by
// rewarding exact identifiers (table names, error strings, refs) and tolerating
// typos.
package textmatch

import "strings"

// Query holds the precomputed token and trigram sets of a query string so one
// query can be scored against many candidates cheaply.
type Query struct {
	tokens   map[string]struct{}
	trigrams map[string]struct{}
}

// NewQuery precomputes the token and trigram sets for a query string.
func NewQuery(s string) Query {
	return Query{
		tokens:   tokenSet(s),
		trigrams: trigramSet(normalizeText(s)),
	}
}

// Score returns how well the query matches a candidate, taking the best of its
// fields. It blends token-set overlap (word matches like "getting started" →
// panda://getting-started) with trigram similarity (typo tolerance like
// "finalty" → finality), plus a boost when every query token appears in a
// field. The result is clamped to [0, 1].
func (q Query) Score(fields ...string) float64 {
	if len(q.tokens) == 0 {
		return 0
	}

	best := 0.0

	for _, field := range fields {
		if field == "" {
			continue
		}

		norm := normalizeText(field)

		score := jaccard(q.tokens, tokenSet(field))
		if dice := diceCoefficient(q.trigrams, trigramSet(norm)); dice > score {
			score = dice
		}

		if containsAllTokens(norm, q.tokens) {
			score += 0.3
		}

		if score > best {
			best = score
		}
	}

	if best > 1 {
		best = 1
	}

	return best
}

// normalizeText lowercases and reduces a string to space-separated alphanumeric
// tokens so URIs, titles, and queries compare on the same footing.
func normalizeText(s string) string {
	var b strings.Builder

	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}

	return strings.Join(strings.Fields(b.String()), " ")
}

func tokenSet(s string) map[string]struct{} {
	fields := strings.Fields(normalizeText(s))
	out := make(map[string]struct{}, len(fields))

	for _, tok := range fields {
		out[tok] = struct{}{}
	}

	return out
}

func containsAllTokens(normalized string, tokens map[string]struct{}) bool {
	for tok := range tokens {
		if !strings.Contains(normalized, tok) {
			return false
		}
	}

	return len(tokens) > 0
}

// trigramSet returns the set of character trigrams of a normalized string (with
// spaces stripped) for fuzzy, typo-tolerant comparison.
func trigramSet(normalized string) map[string]struct{} {
	r := []rune(strings.ReplaceAll(normalized, " ", ""))

	if len(r) < 3 {
		out := make(map[string]struct{}, 1)
		if len(r) > 0 {
			out[string(r)] = struct{}{}
		}

		return out
	}

	out := make(map[string]struct{}, len(r)-2)
	for i := 0; i+3 <= len(r); i++ {
		out[string(r[i:i+3])] = struct{}{}
	}

	return out
}

func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}

	inter := 0

	for k := range a {
		if _, ok := b[k]; ok {
			inter++
		}
	}

	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}

	return float64(inter) / float64(union)
}

func diceCoefficient(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}

	inter := 0

	for k := range a {
		if _, ok := b[k]; ok {
			inter++
		}
	}

	return 2 * float64(inter) / float64(len(a)+len(b))
}
