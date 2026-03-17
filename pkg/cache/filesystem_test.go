package cache

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFilesystemCache(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	c, err := NewFilesystem(dir)
	require.NoError(t, err)

	ctx := context.Background()

	// Miss on empty cache.
	_, found, err := c.Get(ctx, "missing")
	require.NoError(t, err)
	assert.False(t, found)

	// Set and get.
	require.NoError(t, c.Set(ctx, "key1", []byte("value1")))

	data, found, err := c.Get(ctx, "key1")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, []byte("value1"), data)

	// Overwrite.
	require.NoError(t, c.Set(ctx, "key1", []byte("updated")))

	data, found, err = c.Get(ctx, "key1")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, []byte("updated"), data)
}

func TestFilesystemCache_Multi(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	c, err := NewFilesystem(dir)
	require.NoError(t, err)

	ctx := context.Background()

	entries := map[string][]byte{
		"a": []byte("alpha"),
		"b": []byte("beta"),
		"c": []byte("gamma"),
	}

	require.NoError(t, c.SetMulti(ctx, entries))

	result, err := c.GetMulti(ctx, []string{"a", "b", "c", "missing"})
	require.NoError(t, err)
	assert.Len(t, result, 3)
	assert.Equal(t, []byte("alpha"), result["a"])
	assert.Equal(t, []byte("beta"), result["b"])
	assert.Equal(t, []byte("gamma"), result["c"])
}

func TestFilesystemCache_Close(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	c, err := NewFilesystem(dir)
	require.NoError(t, err)

	assert.NoError(t, c.Close())
}
