package cache

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInMemoryCache_GetSet(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	c := NewInMemory(0)

	// Set a value and read it back.
	require.NoError(t, c.Set(ctx, "k1", []byte("v1")))

	val, ok, err := c.Get(ctx, "k1")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, []byte("v1"), val)

	// Overwrite the same key.
	require.NoError(t, c.Set(ctx, "k1", []byte("v2")))

	val, ok, err = c.Get(ctx, "k1")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, []byte("v2"), val)
}

func TestInMemoryCache_GetMiss(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	c := NewInMemory(0)

	val, ok, err := c.Get(ctx, "nonexistent")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Nil(t, val)
}

func TestInMemoryCache_GetMulti_SetMulti(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	c := NewInMemory(0)

	// SetMulti with several entries.
	entries := map[string][]byte{
		"a": []byte("1"),
		"b": []byte("2"),
		"c": []byte("3"),
	}
	require.NoError(t, c.SetMulti(ctx, entries))

	// GetMulti for all keys.
	result, err := c.GetMulti(ctx, []string{"a", "b", "c"})
	require.NoError(t, err)
	assert.Len(t, result, 3)
	assert.Equal(t, []byte("1"), result["a"])
	assert.Equal(t, []byte("2"), result["b"])
	assert.Equal(t, []byte("3"), result["c"])

	// Partial hit: request keys where some are missing.
	result, err = c.GetMulti(ctx, []string{"a", "missing", "c"})
	require.NoError(t, err)
	assert.Len(t, result, 2)
	assert.Equal(t, []byte("1"), result["a"])
	assert.Equal(t, []byte("3"), result["c"])

	// Empty key slice returns empty map.
	result, err = c.GetMulti(ctx, []string{})
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestInMemoryCache_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	c := NewInMemory(0)

	const goroutines = 50
	const iterations = 200

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := range goroutines {
		go func(id int) {
			defer wg.Done()

			key := fmt.Sprintf("key-%d", id)
			val := fmt.Appendf(nil, "val-%d", id)

			for range iterations {
				require.NoError(t, c.Set(ctx, key, val))

				got, ok, err := c.Get(ctx, key)
				require.NoError(t, err)
				// The key we wrote must exist; the value might have been
				// overwritten by SetMulti below, but it must be non-nil.
				assert.True(t, ok)
				assert.NotNil(t, got)
			}

			// Exercise multi operations as well.
			batch := map[string][]byte{
				key:          val,
				key + "-alt": val,
			}
			require.NoError(t, c.SetMulti(ctx, batch))

			result, err := c.GetMulti(ctx, []string{key, key + "-alt"})
			require.NoError(t, err)
			assert.Len(t, result, 2)
		}(i)
	}

	wg.Wait()
}

func TestInMemoryCache_TTLExpiry(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	c := NewInMemory(50 * time.Millisecond)

	require.NoError(t, c.Set(ctx, "k1", []byte("v1")))

	// Immediately readable.
	val, ok, err := c.Get(ctx, "k1")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, []byte("v1"), val)

	// Wait for expiry.
	time.Sleep(60 * time.Millisecond)

	// Should be invisible now.
	val, ok, err = c.Get(ctx, "k1")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Nil(t, val)

	// GetMulti should also skip expired entries.
	result, err := c.GetMulti(ctx, []string{"k1"})
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestInMemoryCache_Close(t *testing.T) {
	t.Parallel()

	c := NewInMemory(0)
	require.NoError(t, c.Close())
}
