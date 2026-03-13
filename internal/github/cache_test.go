package github

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadCacheMissingReturnsNil(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cache, err := LoadCache()
	require.NoError(t, err)
	assert.Nil(t, cache)
}

func TestSaveCacheRoundTripsAndIsFresh(t *testing.T) {
	baseDir := t.TempDir()
	t.Setenv("HOME", baseDir)
	t.Setenv("XDG_CONFIG_HOME", baseDir)

	checkedAt := time.Now().Add(-time.Minute).UTC()
	require.NoError(t, SaveCache(&UpdateCache{
		LatestVersion: "v1.2.3",
		CheckedAt:     checkedAt,
	}))

	cache, err := LoadCache()
	require.NoError(t, err)
	require.NotNil(t, cache)
	assert.Equal(t, "v1.2.3", cache.LatestVersion)
	assert.WithinDuration(t, checkedAt, cache.CheckedAt, time.Second)
	assert.True(t, cache.IsFresh())

	cache.CheckedAt = time.Now().Add(-cacheTTL * 2)
	assert.False(t, cache.IsFresh())
}

func TestLoadCacheReturnsDecodeErrorForInvalidJSON(t *testing.T) {
	baseDir := t.TempDir()
	t.Setenv("HOME", baseDir)
	t.Setenv("XDG_CONFIG_HOME", baseDir)

	require.NoError(t, os.MkdirAll(filepathDir(cachePath()), 0o700))
	require.NoError(t, os.WriteFile(cachePath(), []byte("{invalid"), 0o600))

	cache, err := LoadCache()
	require.Error(t, err)
	assert.Nil(t, cache)
	assert.Contains(t, err.Error(), "decoding update cache")
}

func filepathDir(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}

	return "."
}
