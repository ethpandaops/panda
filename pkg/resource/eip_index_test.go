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
