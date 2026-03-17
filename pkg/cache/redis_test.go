//go:build integration

package cache

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// startRedisCache starts a Redis container and returns a connected RedisCache.
// The caller should defer cleanup and cache.Close().
func startRedisCache(t *testing.T, ctx context.Context) (*RedisCache, func()) {
	t.Helper()

	ctr, err := testcontainers.Run(ctx, "redis:7-alpine",
		testcontainers.WithExposedPorts("6379/tcp"),
		testcontainers.WithWaitStrategy(wait.ForLog("Ready to accept connections")),
	)
	require.NoError(t, err, "failed to start redis container")

	host, err := ctr.Host(ctx)
	require.NoError(t, err)

	mappedPort, err := ctr.MappedPort(ctx, "6379")
	require.NoError(t, err)

	redisURL := fmt.Sprintf("redis://%s:%s/0", host, mappedPort.Port())
	rc, err := NewRedis(redisURL, "test:", 0)
	require.NoError(t, err)

	cleanup := func() {
		_ = rc.Close()
		_ = ctr.Terminate(ctx)
	}

	return rc, cleanup
}

func TestRedisCache_GetSet(t *testing.T) {
	ctx := context.Background()
	rc, cleanup := startRedisCache(t, ctx)
	defer cleanup()

	// Set a value and read it back.
	require.NoError(t, rc.Set(ctx, "k1", []byte("v1")))

	val, ok, err := rc.Get(ctx, "k1")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, []byte("v1"), val)

	// Overwrite the same key.
	require.NoError(t, rc.Set(ctx, "k1", []byte("v2")))

	val, ok, err = rc.Get(ctx, "k1")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, []byte("v2"), val)
}

func TestRedisCache_GetMiss(t *testing.T) {
	ctx := context.Background()
	rc, cleanup := startRedisCache(t, ctx)
	defer cleanup()

	val, ok, err := rc.Get(ctx, "nonexistent")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Nil(t, val)
}

func TestRedisCache_GetMulti_SetMulti(t *testing.T) {
	ctx := context.Background()
	rc, cleanup := startRedisCache(t, ctx)
	defer cleanup()

	// SetMulti with several entries.
	entries := map[string][]byte{
		"a": []byte("1"),
		"b": []byte("2"),
		"c": []byte("3"),
	}
	require.NoError(t, rc.SetMulti(ctx, entries))

	// GetMulti for all keys.
	result, err := rc.GetMulti(ctx, []string{"a", "b", "c"})
	require.NoError(t, err)
	assert.Len(t, result, 3)
	assert.Equal(t, []byte("1"), result["a"])
	assert.Equal(t, []byte("2"), result["b"])
	assert.Equal(t, []byte("3"), result["c"])

	// Partial hit: request keys where some are missing.
	result, err = rc.GetMulti(ctx, []string{"a", "missing", "c"})
	require.NoError(t, err)
	assert.Len(t, result, 2)
	assert.Equal(t, []byte("1"), result["a"])
	assert.Equal(t, []byte("3"), result["c"])

	// Empty key slice returns nil.
	result, err = rc.GetMulti(ctx, []string{})
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestRedisCache_ConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	rc, cleanup := startRedisCache(t, ctx)
	defer cleanup()

	const goroutines = 20
	const iterations = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := range goroutines {
		go func(id int) {
			defer wg.Done()

			key := fmt.Sprintf("key-%d", id)
			val := []byte(fmt.Sprintf("val-%d", id))

			for range iterations {
				require.NoError(t, rc.Set(ctx, key, val))

				got, ok, err := rc.Get(ctx, key)
				require.NoError(t, err)
				assert.True(t, ok)
				assert.NotNil(t, got)
			}

			// Exercise multi operations.
			batch := map[string][]byte{
				key:          val,
				key + "-alt": val,
			}
			require.NoError(t, rc.SetMulti(ctx, batch))

			result, err := rc.GetMulti(ctx, []string{key, key + "-alt"})
			require.NoError(t, err)
			assert.Len(t, result, 2)
		}(i)
	}

	wg.Wait()
}
