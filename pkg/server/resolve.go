package server

import (
	"sort"
	"strings"

	"github.com/ethpandaops/panda/pkg/serverapi"
	"github.com/ethpandaops/panda/pkg/textmatch"
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
	// (a typo'd `runbooks://finalty` should rank the finality runbook, not list
	// all runbooks).
	q := textmatch.NewQuery(refContent(query))

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
				q.Score(res.URI, res.Name, res.Description))
		}
	}

	// Lexical: runbook ref keys (runbooks://{key}).
	if s.runbookRegistry != nil {
		for _, rb := range s.runbookRegistry.All() {
			uri := runbooks.RefURI(rb)
			add(uri, rb.Name, rb.Description, "runbook",
				q.Score(uri, rb.Name, rb.Description))
		}
	}

	// Lexical: consensus-spec ref keys (consensus-specs://{fork}/{topic}).
	if s.specsRegistry != nil {
		for _, spec := range s.specsRegistry.AllSpecs() {
			uri := "consensus-specs://" + spec.Fork + "/" + spec.Topic
			label := spec.Fork + " " + spec.Topic
			add(uri, spec.Title, label, "consensus-spec",
				q.Score(uri, spec.Title, label))
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

// refContent returns the part of a URI-style input after the scheme separator,
// or the whole string when there is no scheme. The scheme is discovery scope,
// not content, so it is excluded from lexical scoring.
func refContent(s string) string {
	if _, after, ok := strings.Cut(s, "://"); ok {
		return after
	}

	return s
}
