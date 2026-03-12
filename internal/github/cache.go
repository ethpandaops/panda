package github

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ethpandaops/panda/pkg/configpath"
)

const (
	cacheFileName = "update-check.json"
	cacheTTL      = 10 * time.Minute
)

// UpdateCache stores the result of a GitHub release check.
type UpdateCache struct {
	LatestVersion string    `json:"latest_version"`
	CheckedAt     time.Time `json:"checked_at"`
}

// LoadCache reads the cache from disk. Returns nil, nil if the file
// does not exist.
func LoadCache() (*UpdateCache, error) {
	data, err := os.ReadFile(cachePath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}

		return nil, fmt.Errorf("reading update cache: %w", err)
	}

	var cache UpdateCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, fmt.Errorf("decoding update cache: %w", err)
	}

	return &cache, nil
}

// SaveCache writes the cache to disk.
func SaveCache(cache *UpdateCache) error {
	path := cachePath()

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating cache directory: %w", err)
	}

	data, err := json.Marshal(cache)
	if err != nil {
		return fmt.Errorf("encoding update cache: %w", err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("writing update cache: %w", err)
	}

	return nil
}

// IsFresh returns true if the cache is younger than the TTL.
func (c *UpdateCache) IsFresh() bool {
	return time.Since(c.CheckedAt) < cacheTTL
}

func cachePath() string {
	return filepath.Join(configpath.DefaultConfigDir(), cacheFileName)
}
