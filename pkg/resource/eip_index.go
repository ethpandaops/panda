package resource

import (
	"fmt"
	"math/bits"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/embedding"
	"github.com/ethpandaops/panda/pkg/types"
)

const exactNumberScore = 1.0

// EIPSearchResult includes the EIP and its similarity score.
type EIPSearchResult struct {
	EIP   types.EIP `json:"eip"`
	Score float64   `json:"similarity_score"`
}

// scored pairs an EIP index with its hybrid score for ranking.
type scored struct {
	eipIdx int
	score  float64
}

// byScoreDesc ranks scored entries by descending score via a concrete
// sort.Interface. Using sort.Sort instead of sort.Slice avoids
// reflect.Swapper's per-swap reflection overhead and the per-query
// comparator-closure allocation. Both entry points run the same
// deterministic pdqsort, so the emitted ordering (and thus the ranked
// EIPs and their scores) is unchanged.
type byScoreDesc []scored

func (s byScoreDesc) Len() int           { return len(s) }
func (s byScoreDesc) Less(i, j int) bool { return s[i].score > s[j].score }
func (s byScoreDesc) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

// indexedEIPChunk holds a reference to an EIP and its embedding vector.
type indexedEIPChunk struct {
	EIPIdx int
	Vector []float32
}

// lowerEIPText holds the lowercased searchable fields of an EIP,
// precomputed at build time so containsText avoids re-lowercasing
// on every query.
type lowerEIPText struct {
	title       string
	description string
	content     string
}

// trigramFilter is a per-EIP Bloom bitset over the character trigrams of the
// EIP's lowercased searchable fields. It answers a conservative necessary
// condition for containsText: if any trigram of the (lowercased) query has its
// bit unset here, the query cannot be a substring of any field, so the exact
// scan can be skipped. A set bit only means "maybe", so a passing filter is
// always followed by the exact containsText check — results are unchanged.
const (
	trigramFilterWords = 64 // 4096-bit filter per EIP
	trigramFilterBits  = trigramFilterWords * 64
	trigramFilterMask  = trigramFilterBits - 1
)

type trigramFilter [trigramFilterWords]uint64

// EIPIndex provides semantic search over EIPs with hybrid scoring.
type EIPIndex struct {
	embedder  embedding.Embedder
	chunks    []indexedEIPChunk
	chunkVecs [][]float64
	eips      []types.EIP
	lowered   []lowerEIPText
	filters   []trigramFilter
	// trigramFreq[b] counts how many EIP filters have bit b set, used to
	// order query probes rarest-first so the per-EIP filter test misses
	// (and early-exits) as soon as possible. Build-time only.
	trigramFreq []uint16
	// invIndex[b] lists the EIP indices whose filter has bit b set (ascending),
	// an inverted index over the trigram filters. The text-boost loop keys it on
	// the query's rarest trigram to visit only candidate EIPs instead of scanning
	// all of them. Build-time only.
	invIndex [][]int32
}

// NewEIPIndex creates a semantic search index from EIPs.
// All chunks are batch-embedded via the remote embedder (which handles
// its own model-aware caching on the proxy side).
func NewEIPIndex(
	log logrus.FieldLogger,
	embedder embedding.Embedder,
	eips []types.EIP,
) (*EIPIndex, error) {
	log = log.WithField("component", "eip_index")

	// Chunk all EIPs and collect texts for batch embedding.
	eipIndices := make([]int, 0, len(eips)*2)
	texts := make([]string, 0, len(eips)*2)

	for eipIdx, eip := range eips {
		for _, chunk := range chunkEIP(eip) {
			eipIndices = append(eipIndices, eipIdx)
			texts = append(texts, chunk)
		}
	}

	log.WithFields(logrus.Fields{
		"eip_count":    len(eips),
		"total_chunks": len(texts),
	}).Info("Embedding EIP chunks")

	vectors, err := embedder.EmbedBatch(texts)
	if err != nil {
		return nil, fmt.Errorf("batch embedding EIP chunks: %w", err)
	}

	chunks := make([]indexedEIPChunk, len(eipIndices))
	for i, eipIdx := range eipIndices {
		chunks[i] = indexedEIPChunk{
			EIPIdx: eipIdx,
			Vector: vectors[i],
		}
	}

	// Precompute float64 chunk vectors once so the per-query dot-product hot
	// loop no longer re-converts every float32 element to float64 on each of
	// the ~450 chunk scorings per query. float64(x) is exact for any float32
	// x, so products (and therefore scores) stay bit-identical.
	chunkVecs := make([][]float64, len(chunks))
	for i, chunk := range chunks {
		v := make([]float64, len(chunk.Vector))
		for j, x := range chunk.Vector {
			v[j] = float64(x)
		}
		chunkVecs[i] = v
	}

	// Precompute lowercased searchable fields once, so containsText does
	// not re-lowercase every EIP's Title, Description and Content per query.
	lowered := make([]lowerEIPText, len(eips))
	for i, eip := range eips {
		lowered[i] = lowerEIPText{
			title:       strings.ToLower(eip.Title),
			description: strings.ToLower(eip.Description),
			content:     strings.ToLower(eip.Content),
		}
	}

	// Precompute a trigram Bloom filter per EIP over its lowercased fields so
	// the per-query text-boost loop can skip the exact scan for EIPs that
	// provably cannot contain the query (see trigramFilter).
	filters := make([]trigramFilter, len(eips))
	for i := range lowered {
		addTrigrams(&filters[i], lowered[i].title)
		addTrigrams(&filters[i], lowered[i].description)
		addTrigrams(&filters[i], lowered[i].content)
	}

	// Count, per filter bit, how many EIP filters set it, so the per-query
	// text-boost loop can test the query's globally-rarest trigram first and
	// early-exit sooner. Purely build-time; does not affect results.
	trigramFreq := make([]uint16, trigramFilterBits)
	invIndex := make([][]int32, trigramFilterBits)
	for i := range filters {
		f := &filters[i]
		for w := 0; w < trigramFilterWords; w++ {
			word := f[w]
			for word != 0 {
				b := w*64 + bits.TrailingZeros64(word)
				trigramFreq[b]++
				invIndex[b] = append(invIndex[b], int32(i))
				word &= word - 1
			}
		}
	}

	log.WithFields(logrus.Fields{
		"eip_count": len(eips),
		"chunks":    len(chunks),
	}).Info("EIP index built")

	return &EIPIndex{
		embedder:    embedder,
		chunks:      chunks,
		chunkVecs:   chunkVecs,
		eips:        eips,
		lowered:     lowered,
		filters:     filters,
		trigramFreq: trigramFreq,
		invIndex:    invIndex,
	}, nil
}

// Search returns the top-k semantically similar EIPs for a query.
// Uses hybrid scoring: vector similarity + exact text match boost.
func (idx *EIPIndex) Search(query string, limit int) ([]EIPSearchResult, error) {
	queryVec, err := idx.embedder.Embed(query)
	if err != nil {
		return nil, fmt.Errorf("embedding query: %w", err)
	}

	// Score all chunks and deduplicate to the best score per EIP in a
	// single pass. eipIdx is dense over [0, len(eips)), so a plain slice
	// replaces the per-query map hashing/allocation. best[i] starts at 0 and
	// is only ever assigned strictly-positive values (a chunk score s is
	// written only when s > best[i] >= 0, so s > 0; the exact-number and
	// text-match branches assign the positive constants exactNumberScore/
	// textMatchBase or add a positive boost). Presence in the result set is
	// therefore exactly best[i] > 0, matching the original map[int]float64
	// "key exists iff written" semantics without a separate present array.
	best := make([]float64, len(idx.eips))

	// Convert the query vector to float64 once. The chunk vectors are already
	// stored as float64 (idx.chunkVecs), so both dot-product operands are
	// float64 and no per-element float32->float64 conversion happens inside the
	// hot loop; float64(queryVec[i]) is the same value however often computed,
	// so scores are preserved.
	queryF64 := make([]float64, len(queryVec))
	for i, v := range queryVec {
		queryF64[i] = float64(v)
	}

	for i := range idx.chunkVecs {
		cv := idx.chunkVecs[i]

		// Dot product inlined from the former dotProduct64: that standalone
		// function exceeded the inliner budget, so each of the ~450 chunks per
		// query paid a real call plus a length re-check. Inlining keeps the
		// identical 4-way accumulator split (same operations in the same order),
		// so every product and score stays bit-identical.
		var s float64
		if n := len(queryF64); len(cv) == n {
			a := queryF64[:n]
			b := cv[:n]

			var s0, s1, s2, s3 float64

			j := 0
			for ; j <= n-4; j += 4 {
				s0 += a[j] * b[j]
				s1 += a[j+1] * b[j+1]
				s2 += a[j+2] * b[j+2]
				s3 += a[j+3] * b[j+3]
			}

			for ; j < n; j++ {
				s0 += a[j] * b[j]
			}

			s = s0 + s1 + s2 + s3
		}

		eipIdx := idx.chunks[i].EIPIdx
		if s > best[eipIdx] {
			best[eipIdx] = s
		}
	}

	// Exact EIP number match: if the query references a specific EIP number,
	// pin it to the top regardless of vector similarity.
	if num := extractEIPNumber(query); num > 0 {
		for eipIdx, eip := range idx.eips {
			if eip.Number == num {
				best[eipIdx] = exactNumberScore
			}
		}
	}

	// Hybrid boost: add text match score for queries > 4 chars.
	if len(query) > 4 {
		lowerQuery := strings.ToLower(query)

		// Precompute the query's trigram bit probes once and reuse them across
		// all ~300 EIP filters, instead of re-hashing the query's trigrams inside
		// filterMayContain for every EIP. The bits tested are identical, so the
		// ranked EIPs and scores are unchanged.
		probes := queryTrigrams(lowerQuery)

		if len(probes) == 0 {
			// No trigrams (query shorter than 3 bytes after lowercasing): the
			// empty filter passes for every EIP, so fall back to the exact scan
			// over all EIPs, matching filterMayContainProbes(nil)==true.
			for eipIdx := range idx.eips {
				if containsText(idx.lowered[eipIdx], lowerQuery) {
					if best[eipIdx] > 0 {
						best[eipIdx] += textMatchBoost
					} else {
						best[eipIdx] = textMatchBase
					}
				}
			}
		} else {
			// Move the globally-rarest trigram to the front so each per-EIP filter
			// test is most likely to fail on its first probe and early-exit. This
			// only reorders the AND over probes, so the pass/fail result — and thus
			// the boosted EIPs and their scores — is unchanged.
			if len(probes) > 1 {
				minI := 0
				minF := idx.trigramFreq[probes[0].bit]
				for i := 1; i < len(probes); i++ {
					if f := idx.trigramFreq[probes[i].bit]; f < minF {
						minF = f
						minI = i
					}
				}
				probes[0], probes[minI] = probes[minI], probes[0]
			}

			// filterMayContainProbes can only pass when the rarest trigram's bit
			// is set, so the only EIPs that can be boosted are exactly those the
			// inverted index lists for that bit. Iterating that candidate set
			// (instead of scanning all EIPs) tests a strict subset — every skipped
			// EIP lacks the rarest bit and would have failed the filter anyway — so
			// the boosted EIPs and their scores are unchanged.
			for _, eipIdx32 := range idx.invIndex[probes[0].bit] {
				eipIdx := int(eipIdx32)
				if filterMayContainProbes(&idx.filters[eipIdx], probes) &&
					containsText(idx.lowered[eipIdx], lowerQuery) {
					if best[eipIdx] > 0 {
						best[eipIdx] += textMatchBoost
					} else {
						best[eipIdx] = textMatchBase
					}
				}
			}
		}
	}

	// Select the top-`limit` present EIPs by descending score using a
	// bounded min-heap instead of building and fully sorting the entire
	// present set, turning the O(n log n) sort over all present EIPs into
	// an O(n log limit) partial selection. The surviving `limit` entries
	// are then sorted descending for output. With no score ties in the
	// ranked region (the same property the prior full sort relied on to be
	// deterministic), the selected EIPs and their order are identical.
	k := limit
	if k > len(idx.eips) {
		k = len(idx.eips)
	}
	if k < 0 {
		k = 0
	}

	top := make([]scored, 0, k)
	for eipIdx := range idx.eips {
		if best[eipIdx] <= 0 {
			continue
		}

		sc := scored{eipIdx: eipIdx, score: best[eipIdx]}

		if len(top) < k {
			top = append(top, sc)
			i := len(top) - 1
			for i > 0 {
				p := (i - 1) / 2
				if top[p].score <= top[i].score {
					break
				}
				top[p], top[i] = top[i], top[p]
				i = p
			}
		} else if k > 0 && sc.score > top[0].score {
			top[0] = sc
			i := 0
			for {
				l := 2*i + 1
				r := 2*i + 2
				m := i
				if l < k && top[l].score < top[m].score {
					m = l
				}
				if r < k && top[r].score < top[m].score {
					m = r
				}
				if m == i {
					break
				}
				top[i], top[m] = top[m], top[i]
				i = m
			}
		}
	}

	sort.Sort(byScoreDesc(top))

	out := make([]EIPSearchResult, 0, len(top))
	for _, sv := range top {
		out = append(out, EIPSearchResult{
			EIP:   idx.eips[sv.eipIdx],
			Score: sv.score,
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

func containsText(l lowerEIPText, lowerQuery string) bool {
	return strings.Contains(l.title, lowerQuery) ||
		strings.Contains(l.description, lowerQuery) ||
		strings.Contains(l.content, lowerQuery)
}

// trigramBit maps a character trigram to a bit position in a trigramFilter.
func trigramBit(a, b, c byte) uint32 {
	h := uint32(a)
	h = h*31 + uint32(b)
	h = h*31 + uint32(c)

	return h & trigramFilterMask
}

// addTrigrams sets the bit for every character trigram of s in f.
func addTrigrams(f *trigramFilter, s string) {
	for j := 0; j+3 <= len(s); j++ {
		h := trigramBit(s[j], s[j+1], s[j+2])
		f[h>>6] |= 1 << (h & 63)
	}
}

// trigramProbe is a precomputed word index and bit mask for one query
// trigram, so an EIP filter can be tested without re-hashing the query.
type trigramProbe struct {
	word uint32
	mask uint64
	bit  uint32
}

// queryTrigrams precomputes the filter bit positions for every character
// trigram of q once, so filterMayContainProbes can test many EIP filters
// without recomputing q's trigram hashes for each one. It mirrors the
// hashing in addTrigrams/filterMayContain exactly.
func queryTrigrams(q string) []trigramProbe {
	if len(q) < 3 {
		return nil
	}

	probes := make([]trigramProbe, 0, len(q)-2)
	for j := 0; j+3 <= len(q); j++ {
		h := trigramBit(q[j], q[j+1], q[j+2])
		probes = append(probes, trigramProbe{word: h >> 6, mask: 1 << (h & 63), bit: h})
	}

	return probes
}

// filterMayContainProbes is the precomputed-query form of filterMayContain:
// it tests the same filter bits (via probes) and returns the same result,
// but reads the probes instead of re-hashing the query for this filter.
func filterMayContainProbes(f *trigramFilter, probes []trigramProbe) bool {
	for _, p := range probes {
		if f[p.word]&p.mask == 0 {
			return false
		}
	}

	return true
}

var eipNumberRe = regexp.MustCompile(`(?i)eip[- ]?(\d+)`)

// hasEIPFold reports whether s contains the ASCII letters "eip" in any case.
// This is a necessary condition for eipNumberRe to match (the pattern begins
// with a case-insensitive "eip"), so gating the regexp on it lets
// extractEIPNumber skip the comparatively expensive regexp execution for the
// common query that mentions no EIP at all, without changing which queries the
// regex ultimately matches.
func hasEIPFold(s string) bool {
	for i := 0; i+3 <= len(s); i++ {
		if (s[i]|0x20) == 'e' && (s[i+1]|0x20) == 'i' && (s[i+2]|0x20) == 'p' {
			return true
		}
	}

	return false
}

// extractEIPNumber parses an EIP number from a query string.
// Matches patterns like "eip-4844", "EIP 4844", "EIP4844",
// and bare numbers like "4844".
func extractEIPNumber(query string) int {
	// eipNumberRe can only match when "eip" (case-insensitive) is present, so
	// gate the regexp on a cheap byte scan; queries without it fall straight
	// through to the bare-number check below with identical results.
	if hasEIPFold(query) {
		if m := eipNumberRe.FindStringSubmatch(query); len(m) == 2 {
			n, err := strconv.Atoi(m[1])
			if err == nil {
				return n
			}
		}
	}

	// Bare number: if the entire query is digits, treat it as an EIP number.
	if n, err := strconv.Atoi(strings.TrimSpace(query)); err == nil && n > 0 {
		return n
	}

	return 0
}
