package cache

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
)

// FilesystemCache is a key-value cache backed by files in a directory.
// Keys are SHA256-hashed to produce safe filenames.
type FilesystemCache struct {
	dir string
}

// Compile-time interface check.
var _ Cache = (*FilesystemCache)(nil)

// NewFilesystem creates a new filesystem-backed cache in the given directory.
// The directory is created if it does not exist.
func NewFilesystem(dir string) (*FilesystemCache, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating cache directory: %w", err)
	}

	return &FilesystemCache{dir: dir}, nil
}

// Get retrieves a value by key.
func (c *FilesystemCache) Get(_ context.Context, key string) ([]byte, bool, error) {
	data, err := os.ReadFile(c.path(key))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}

		return nil, false, fmt.Errorf("reading cache file: %w", err)
	}

	return data, true, nil
}

// Set stores a value by key using atomic write (temp file + rename).
func (c *FilesystemCache) Set(_ context.Context, key string, value []byte) error {
	target := c.path(key)

	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, value, 0o644); err != nil {
		return fmt.Errorf("writing temp cache file: %w", err)
	}

	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)

		return fmt.Errorf("renaming cache file: %w", err)
	}

	return nil
}

// GetMulti retrieves multiple values by keys.
func (c *FilesystemCache) GetMulti(ctx context.Context, keys []string) (map[string][]byte, error) {
	result := make(map[string][]byte, len(keys))

	for _, key := range keys {
		data, found, err := c.Get(ctx, key)
		if err != nil {
			return nil, err
		}

		if found {
			result[key] = data
		}
	}

	return result, nil
}

// SetMulti stores multiple key-value pairs.
func (c *FilesystemCache) SetMulti(ctx context.Context, entries map[string][]byte) error {
	for key, value := range entries {
		if err := c.Set(ctx, key, value); err != nil {
			return err
		}
	}

	return nil
}

// Close is a no-op for the filesystem cache.
func (c *FilesystemCache) Close() error {
	return nil
}

func (c *FilesystemCache) path(key string) string {
	h := sha256.Sum256([]byte(key))

	return filepath.Join(c.dir, fmt.Sprintf("%x.bin", h))
}
