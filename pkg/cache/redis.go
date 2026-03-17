package cache

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisCache is a cache backed by Redis.
type RedisCache struct {
	client *redis.Client
	prefix string
	ttl    time.Duration
}

// Compile-time interface check.
var _ Cache = (*RedisCache)(nil)

// NewRedis creates a new Redis-backed cache.
// The prefix is prepended to all keys (e.g., "panda:embed:").
// A zero TTL means entries never expire.
func NewRedis(redisURL, prefix string, ttl time.Duration) (*RedisCache, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parsing redis URL: %w", err)
	}

	client := redis.NewClient(opts)

	return &RedisCache{
		client: client,
		prefix: prefix,
		ttl:    ttl,
	}, nil
}

// Get retrieves a value by key.
func (c *RedisCache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	val, err := c.client.Get(ctx, c.prefix+key).Bytes()
	if err == redis.Nil {
		return nil, false, nil
	}

	if err != nil {
		return nil, false, fmt.Errorf("redis get %q: %w", key, err)
	}

	return val, true, nil
}

// Set stores a value by key.
func (c *RedisCache) Set(ctx context.Context, key string, value []byte) error {
	if err := c.client.Set(ctx, c.prefix+key, value, c.ttl).Err(); err != nil {
		return fmt.Errorf("redis set %q: %w", key, err)
	}

	return nil
}

// GetMulti retrieves multiple values by keys.
func (c *RedisCache) GetMulti(ctx context.Context, keys []string) (map[string][]byte, error) {
	if len(keys) == 0 {
		return nil, nil
	}

	prefixedKeys := make([]string, len(keys))
	for i, key := range keys {
		prefixedKeys[i] = c.prefix + key
	}

	vals, err := c.client.MGet(ctx, prefixedKeys...).Result()
	if err != nil {
		return nil, fmt.Errorf("redis mget: %w", err)
	}

	result := make(map[string][]byte, len(keys))
	for i, val := range vals {
		if val == nil {
			continue
		}

		if s, ok := val.(string); ok {
			result[keys[i]] = []byte(s)
		}
	}

	return result, nil
}

// SetMulti stores multiple key-value pairs.
func (c *RedisCache) SetMulti(ctx context.Context, entries map[string][]byte) error {
	if len(entries) == 0 {
		return nil
	}

	pipe := c.client.Pipeline()
	for key, value := range entries {
		pipe.Set(ctx, c.prefix+key, value, c.ttl)
	}

	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("redis pipeline set: %w", err)
	}

	return nil
}

// Close closes the Redis client connection.
func (c *RedisCache) Close() error {
	return c.client.Close()
}
