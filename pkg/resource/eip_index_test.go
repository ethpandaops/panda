package resource

import (
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/embedding"
	"github.com/ethpandaops/panda/pkg/types"
)

func TestEIPSearchVectorReuse(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	modelPath := "../../models/all-MiniLM-L6-v2"
	embedder, err := embedding.New(modelPath)
	if err != nil {
		t.Skipf("embedding model not available at %s: %v", modelPath, err)
	}
	defer func() { _ = embedder.Close() }()

	log := logrus.New()

	eips := []types.EIP{
		{Number: 1, Title: "Test EIP One", Description: "First test proposal", Content: "Details about EIP one."},
		{Number: 2, Title: "Test EIP Two", Description: "Second test proposal", Content: "Details about EIP two."},
		{Number: 3, Title: "Test EIP Three", Description: "Third test proposal", Content: "Details about EIP three."},
	}

	// First build: no cached vectors, all must be embedded.
	idx1, vectors1, err := NewEIPIndex(log, embedder, eips, nil)
	require.NoError(t, err)
	require.NotNil(t, idx1)
	assert.NotEmpty(t, vectors1)

	// Second build: all vectors cached, all should be reused.
	idx2, vectors2, err := NewEIPIndex(log, embedder, eips, vectors1)
	require.NoError(t, err)
	require.NotNil(t, idx2)
	assert.Equal(t, len(vectors1), len(vectors2))

	// Third build: change one EIP, should re-embed only that one.
	eips[2].Description = "Modified third proposal description"
	_, vectors3, err := NewEIPIndex(log, embedder, eips, vectors2)
	require.NoError(t, err)

	// EIP-1 and EIP-2 vectors should match; EIP-3 should differ.
	assert.Equal(t, vectors2["1:0"].TextHash, vectors3["1:0"].TextHash)
	assert.Equal(t, vectors2["2:0"].TextHash, vectors3["2:0"].TextHash)
	assert.NotEqual(t, vectors2["3:0"].TextHash, vectors3["3:0"].TextHash)
}
