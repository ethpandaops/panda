package cache

import (
	"context"
	"sync"
	"time"
)

// memEntry stores a value with an optional expiry time.
type memEntry struct {
	value     []byte
	expiresAt time.Time
}

// InMemoryCache is a thread-safe in-memory cache backed by a map.
type InMemoryCache struct {
	mu    sync.RWMutex
	items map[string]memEntry
	ttl   time.Duration
}

// Compile-time interface check.
var _ Cache = (*InMemoryCache)(nil)

// NewInMemory creates a new in-memory cache.
// A zero TTL means entries never expire.
func NewInMemory(ttl time.Duration) *InMemoryCache {
	return &InMemoryCache{
		items: make(map[string]memEntry, 256),
		ttl:   ttl,
	}
}

// Get retrieves a value by key, skipping expired entries.
func (c *InMemoryCache) Get(_ context.Context, key string) ([]byte, bool, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.items[key]
	if !ok {
		return nil, false, nil
	}

	if c.expired(entry) {
		return nil, false, nil
	}

	return entry.value, true, nil
}

// Set stores a value by key.
func (c *InMemoryCache) Set(_ context.Context, key string, value []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items[key] = c.newEntry(value)

	return nil
}

// GetMulti retrieves multiple values by keys, skipping expired entries.
func (c *InMemoryCache) GetMulti(_ context.Context, keys []string) (map[string][]byte, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make(map[string][]byte, len(keys))
	for _, key := range keys {
		entry, ok := c.items[key]
		if ok && !c.expired(entry) {
			result[key] = entry.value
		}
	}

	return result, nil
}

// SetMulti stores multiple key-value pairs.
func (c *InMemoryCache) SetMulti(_ context.Context, entries map[string][]byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	for key, value := range entries {
		c.items[key] = c.newEntry(value)
	}

	return nil
}

// Close is a no-op for the in-memory cache.
func (c *InMemoryCache) Close() error {
	return nil
}

func (c *InMemoryCache) newEntry(value []byte) memEntry {
	e := memEntry{value: value}
	if c.ttl > 0 {
		e.expiresAt = time.Now().Add(c.ttl)
	}

	return e
}

func (c *InMemoryCache) expired(e memEntry) bool {
	return c.ttl > 0 && time.Now().After(e.expiresAt)
}
