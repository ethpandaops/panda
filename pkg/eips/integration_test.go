//go:build integration

package eips

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchAndParse(t *testing.T) {
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
