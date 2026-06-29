package server

import (
	"sort"
	"strings"

	"github.com/ethpandaops/panda/pkg/serverapi"
	"github.com/ethpandaops/panda/runbooks"
)

const (
	// defaultResolveLimit caps how many candidates resource discovery returns.
	defaultResolveLimit = 8
	// minResolveScore drops weak lexical matches so suggestions stay relevant.
	minResolveScore = 0.2
)

// resolveResources returns ranked resource candidates for a fuzzy or partial
// resource path, matching lexically (token + trigram similarity) over every
// known resource path: static URIs and runbook / consensus-spec ref keys. It
// resolves the right path when the caller guessed wrong (typo, partial, wrong
// scheme) without knowing it exactly.
//
// It is deliberately path-resolution only — discovery "by meaning" is the
// search tool's job. Numeric EIP refs are not lexically discoverable; reach
// those via search.
func (s *service) resolveResources(query string, limit int) []serverapi.ResourceCandidate {
	if limit <= 0 {
		limit = defaultResolveLimit
	}

	// Score on the key portion of a URI input, not the scheme: the scheme is
	// scope, not content, so matching it would surface every same-scheme path
	// (a typo'd `runbooks://finalty` should rank finality_delay, not list all
	// runbooks).
	content := refContent(query)
	qTokens := tokenSet(content)
	qTrigrams := trigramSet(normalizeText(content))

	byURI := map[string]*serverapi.ResourceCandidate{}

	add := func(uri, title, desc, kind string, score float64) {
		if uri == "" || score <= 0 {
			return
		}

		if existing, ok := byURI[uri]; ok {
			if score > existing.Score {
				existing.Score = score
			}

			return
		}

		byURI[uri] = &serverapi.ResourceCandidate{
			URI:         uri,
			Title:       title,
			Description: desc,
			Kind:        kind,
			Score:       score,
		}
	}

	// Lexical: static resources (panda://, networks://, datasets://, …).
	if s.resourceRegistry != nil {
		for _, res := range s.resourceRegistry.ListStatic() {
			add(res.URI, res.Name, res.Description, "resource",
				lexicalScore(qTokens, qTrigrams, res.URI, res.Name, res.Description))
		}
	}

	// Lexical: runbook ref keys (runbooks://{key}).
	if s.runbookRegistry != nil {
		for _, rb := range s.runbookRegistry.All() {
			uri := runbooks.RefURI(rb)
			add(uri, rb.Name, rb.Description, "runbook",
				lexicalScore(qTokens, qTrigrams, uri, rb.Name, rb.Description))
		}
	}

	// Lexical: consensus-spec ref keys (consensus-specs://{fork}/{topic}).
	if s.specsRegistry != nil {
		for _, spec := range s.specsRegistry.AllSpecs() {
			uri := "consensus-specs://" + spec.Fork + "/" + spec.Topic
			label := spec.Fork + " " + spec.Topic
			add(uri, spec.Title, label, "consensus-spec",
				lexicalScore(qTokens, qTrigrams, uri, spec.Title, label))
		}
	}

	candidates := make([]serverapi.ResourceCandidate, 0, len(byURI))
	for _, c := range byURI {
		if c.Score >= minResolveScore {
			candidates = append(candidates, *c)
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}

		return candidates[i].URI < candidates[j].URI
	})

	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	return candidates
}

// lexicalScore scores how well a query matches a candidate, taking the best of
// its fields. It blends token-set overlap (word matches like "getting started" →
// panda://getting-started) with trigram similarity (typo tolerance like "finalty"
// → finality_delay), plus a boost when the query is a substring of a field.
func lexicalScore(qTokens map[string]struct{}, qTrigrams map[string]struct{}, fields ...string) float64 {
	if len(qTokens) == 0 {
		return 0
	}

	best := 0.0

	for _, field := range fields {
		if field == "" {
			continue
		}

		norm := normalizeText(field)

		score := jaccard(qTokens, tokenSet(field))
		if dice := diceCoefficient(qTrigrams, trigramSet(norm)); dice > score {
			score = dice
		}

		if containsAllTokens(norm, qTokens) {
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

// refContent returns the part of a URI-style input after the scheme separator,
// or the whole string when there is no scheme. The scheme is discovery scope,
// not content, so it is excluded from lexical scoring.
func refContent(s string) string {
	if i := strings.Index(s, "://"); i >= 0 {
		return s[i+len("://"):]
	}

	return s
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
	out := map[string]struct{}{}
	for _, tok := range strings.Fields(normalizeText(s)) {
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
	out := map[string]struct{}{}

	if len(r) < 3 {
		if len(r) > 0 {
			out[string(r)] = struct{}{}
		}

		return out
	}

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
