package searchruntime

import (
	"fmt"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/resource"
	"github.com/ethpandaops/panda/pkg/types"
)

type fakeEmbedder struct {
	closed   bool
	closeErr error
}

func (f *fakeEmbedder) Embed(_ string) ([]float32, error) {
	return []float32{1, 0, 0}, nil
}

func (f *fakeEmbedder) EmbedBatch(texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1, 0, 0}
	}

	return out, nil
}

func (f *fakeEmbedder) Close() error {
	f.closed = true

	return f.closeErr
}

func newExampleIndex(t *testing.T, emb *fakeEmbedder) *resource.ExampleIndex {
	t.Helper()

	examples := map[string]types.ExampleCategory{
		"blocks": {
			Name: "Blocks",
			Examples: []types.Example{
				{Name: "recent-blocks", Description: "Recent blocks", Query: "SELECT 1"},
			},
		},
	}

	idx, err := resource.NewExampleIndex(logrus.New(), emb, examples)
	require.NoError(t, err)

	return idx
}

func TestRuntimeCloseNil(t *testing.T) {
	t.Parallel()

	var r *Runtime
	assert.NoError(t, r.Close())
}

func TestRuntimeCloseEmpty(t *testing.T) {
	t.Parallel()

	r := &Runtime{}
	assert.NoError(t, r.Close())
}

func TestRuntimeCloseClosesSharedEmbedderWithIndex(t *testing.T) {
	t.Parallel()

	// In production the example index and the runtime share one embedder; the
	// runtime owns it and closes it once. Swapping indices must not close it.
	embedder := &fakeEmbedder{}

	r := &Runtime{
		ExampleIndex: resource.NewRefreshableExampleIndex(newExampleIndex(t, embedder)),
		embedder:     embedder,
	}

	require.NoError(t, r.Close())

	assert.True(t, embedder.closed, "runtime should close the shared embedder")
}

func TestRuntimeCloseEmbedderOnly(t *testing.T) {
	t.Parallel()

	embedder := &fakeEmbedder{}
	r := &Runtime{embedder: embedder}

	require.NoError(t, r.Close())
	assert.True(t, embedder.closed)
}

func TestRuntimeCloseEmbedderError(t *testing.T) {
	t.Parallel()

	embedder := &fakeEmbedder{closeErr: fmt.Errorf("close failed")}
	r := &Runtime{embedder: embedder}

	require.Error(t, r.Close())
}
