package resource

import (
	"crypto/sha256"
	"encoding/binary"
	"math"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/embedding"
	"github.com/ethpandaops/panda/pkg/types"
)

// stubEmbedder returns deterministic vectors based on text hash.
type stubEmbedder struct {
	dim int
}

var _ embedding.Embedder = (*stubEmbedder)(nil)

func (s *stubEmbedder) Embed(text string) ([]float32, error) {
	h := sha256.Sum256([]byte(text))
	vec := make([]float32, s.dim)

	for i := range vec {
		bits := binary.LittleEndian.Uint32(h[((i * 4) % len(h)):])
		vec[i] = float32(bits) / float32(math.MaxUint32)
	}

	// L2-normalize.
	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}

	norm = math.Sqrt(norm)
	for i := range vec {
		vec[i] = float32(float64(vec[i]) / norm)
	}

	return vec, nil
}

func (s *stubEmbedder) EmbedBatch(texts []string) ([][]float32, error) {
	vecs := make([][]float32, len(texts))
	for i, t := range texts {
		v, err := s.Embed(t)
		if err != nil {
			return nil, err
		}

		vecs[i] = v
	}

	return vecs, nil
}

func (s *stubEmbedder) Close() error { return nil }

func TestExtractEIPNumber(t *testing.T) {
	t.Parallel()

	tests := []struct {
		query    string
		expected int
	}{
		{"eip-4844", 4844},
		{"EIP-4844", 4844},
		{"eip 4844", 4844},
		{"EIP4844", 4844},
		{"erc-20", 20},
		{"ERC 721", 721},
		{"erc20", 20},
		{"4844", 0},
		{"blob transactions", 0},
		{"", 0},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, extractEIPNumber(tt.query))
		})
	}
}

func TestEIPSearchExactNumberMatch(t *testing.T) {
	t.Parallel()

	embedder := &stubEmbedder{dim: 8}
	log := logrus.New()

	eips := []types.EIP{
		{Number: 4844, Title: "Shard Blob Transactions", Description: "Proto-Danksharding"},
		{Number: 4834, Title: "Some Other EIP", Description: "Unrelated proposal"},
		{Number: 1844, Title: "Another EIP", Description: "Also unrelated"},
	}

	idx, err := NewEIPIndex(log, embedder, eips)
	require.NoError(t, err)

	results, err := idx.Search("eip-4844", 3)
	require.NoError(t, err)
	require.NotEmpty(t, results)

	// EIP-4844 must be the top result with the exact number score.
	assert.Equal(t, 4844, results[0].EIP.Number)
	assert.Equal(t, exactNumberScore, results[0].Score)
}

func TestEIPSearchIndex(t *testing.T) {
	t.Parallel()

	embedder := &stubEmbedder{dim: 8}
	log := logrus.New()

	eips := []types.EIP{
		{Number: 1, Title: "Test EIP One", Description: "First test proposal", Content: "Details about EIP one."},
		{Number: 2, Title: "Test EIP Two", Description: "Second test proposal", Content: "Details about EIP two."},
		{Number: 3, Title: "Test EIP Three", Description: "Third test proposal", Content: "Details about EIP three."},
	}

	idx, err := NewEIPIndex(log, embedder, eips)
	require.NoError(t, err)
	require.NotNil(t, idx)
	assert.Len(t, idx.chunks, 3)
	assert.Len(t, idx.eips, 3)

	// Search should return results ordered by similarity.
	results, err := idx.Search("test proposal", 3)
	require.NoError(t, err)
	assert.Len(t, results, 3)

	for _, r := range results {
		assert.Greater(t, r.Score, 0.0)
	}
}
