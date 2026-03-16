package eips

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/types"
)

func TestFetchAndParse(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx := context.Background()
	f := newFetcher()

	sha, err := f.latestCommitSHA(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, sha)

	eips, err := f.fetchAll(ctx)
	require.NoError(t, err)
	assert.Greater(t, len(eips), 500, "expected at least 500 EIPs")

	found := false

	for _, eip := range eips {
		if eip.Number == 1559 {
			found = true
			assert.Equal(t, "Final", eip.Status)
			assert.Equal(t, "Core", eip.Category)
			assert.NotEmpty(t, eip.Content)

			break
		}
	}

	assert.True(t, found, "EIP-1559 should be in the results")
}

func TestRegistryWithCache(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx := context.Background()
	log := logrus.New()
	tmpDir := t.TempDir()

	reg1, err := NewRegistry(ctx, log, tmpDir)
	require.NoError(t, err)
	assert.Greater(t, reg1.Count(), 500)

	_, err = os.Stat(filepath.Join(tmpDir, "eips.json"))
	require.NoError(t, err, "cache file should exist")

	reg2, err := NewRegistry(ctx, log, tmpDir)
	require.NoError(t, err)
	assert.Equal(t, reg1.Count(), reg2.Count())

	assert.GreaterOrEqual(t, len(reg2.Statuses()), 3)
	assert.GreaterOrEqual(t, len(reg2.Categories()), 2)
	assert.GreaterOrEqual(t, len(reg2.Types()), 2)
}

func TestVectorCachePersistence(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx := context.Background()
	log := logrus.New()
	tmpDir := t.TempDir()

	reg, err := NewRegistry(ctx, log, tmpDir)
	require.NoError(t, err)

	assert.Nil(t, reg.CachedVectors())

	vectors := map[string]types.EIPVector{
		"1559:0": {TextHash: "abc123", Vector: []float32{0.1, 0.2, 0.3}},
		"4844:0": {TextHash: "def456", Vector: []float32{0.4, 0.5, 0.6}},
	}

	require.NoError(t, reg.SaveVectors(vectors))

	reg2, err := NewRegistry(ctx, log, tmpDir)
	require.NoError(t, err)

	cached := reg2.CachedVectors()
	require.NotNil(t, cached)
	assert.Len(t, cached, 2)
	assert.Equal(t, "abc123", cached["1559:0"].TextHash)
	assert.Equal(t, []float32{0.4, 0.5, 0.6}, cached["4844:0"].Vector)
}
