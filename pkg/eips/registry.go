package eips

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/mcp/pkg/types"
)

type cacheData struct {
	CommitSHA string                     `json:"commit_sha"`
	FetchedAt time.Time                  `json:"fetched_at"`
	EIPs      []types.EIP                `json:"eips"`
	Vectors   map[string]types.EIPVector `json:"vectors,omitempty"`
}

// Registry holds fetched EIPs and provides access for indexing and search.
type Registry struct {
	log      logrus.FieldLogger
	cacheDir string
	eips     []types.EIP
	byNumber map[int]*types.EIP
	cache    *cacheData
	mu       sync.RWMutex
}

// NewRegistry creates a new EIP registry, loading from cache or fetching from GitHub.
func NewRegistry(ctx context.Context, log logrus.FieldLogger, cacheDir string) (*Registry, error) {
	log = log.WithField("component", "eip_registry")

	if cacheDir == "" {
		userCache, err := os.UserCacheDir()
		if err != nil {
			return nil, fmt.Errorf("getting user cache dir: %w", err)
		}

		cacheDir = filepath.Join(userCache, "ethpandaops-mcp", "eips")
	}

	f := newFetcher(log)

	// Check latest commit SHA from GitHub.
	latestSHA, err := f.latestCommitSHA(ctx)
	if err != nil {
		log.WithError(err).Warn("Failed to check EIP updates from GitHub, trying cache")

		cache, cacheErr := loadCache(cacheDir)
		if cacheErr != nil {
			return nil, fmt.Errorf("checking EIP updates: %w (no cache available: %v)", err, cacheErr)
		}

		log.WithField("eip_count", len(cache.EIPs)).Info("Loaded EIPs from stale cache")

		return newFromCache(log, cacheDir, cache), nil
	}

	// Check if cache is up to date.
	cache, cacheErr := loadCache(cacheDir)
	if cacheErr == nil && cache.CommitSHA == latestSHA {
		log.WithFields(logrus.Fields{
			"commit_sha": shortSHA(latestSHA),
			"eip_count":  len(cache.EIPs),
		}).Info("EIP cache is up to date")

		return newFromCache(log, cacheDir, cache), nil
	}

	// Fetch all EIPs from GitHub.
	eips, err := f.fetchAll(ctx)
	if err != nil {
		if cache != nil {
			log.WithError(err).Warn("Failed to fetch EIPs, using stale cache")

			return newFromCache(log, cacheDir, cache), nil
		}

		return nil, fmt.Errorf("fetching EIPs: %w", err)
	}

	sort.Slice(eips, func(i, j int) bool {
		return eips[i].Number < eips[j].Number
	})

	// Preserve cached vectors from previous fetch — they'll be validated
	// by text hash during index building and reused when unchanged.
	var oldVectors map[string]types.EIPVector
	if cache != nil {
		oldVectors = cache.Vectors
	}

	newCache := &cacheData{
		CommitSHA: latestSHA,
		FetchedAt: time.Now(),
		EIPs:      eips,
		Vectors:   oldVectors,
	}

	if err := saveCache(cacheDir, newCache); err != nil {
		log.WithError(err).Warn("Failed to save EIP cache")
	}

	log.WithFields(logrus.Fields{
		"eip_count":  len(eips),
		"commit_sha": shortSHA(latestSHA),
	}).Info("EIP registry loaded from GitHub")

	return newFromCache(log, cacheDir, newCache), nil
}

func newFromCache(log logrus.FieldLogger, cacheDir string, cache *cacheData) *Registry {
	byNumber := make(map[int]*types.EIP, len(cache.EIPs))
	for i := range cache.EIPs {
		byNumber[cache.EIPs[i].Number] = &cache.EIPs[i]
	}

	return &Registry{
		log:      log,
		cacheDir: cacheDir,
		eips:     cache.EIPs,
		byNumber: byNumber,
		cache:    cache,
	}
}

// All returns all loaded EIPs.
func (r *Registry) All() []types.EIP {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]types.EIP, len(r.eips))
	copy(result, r.eips)

	return result
}

// Count returns the number of loaded EIPs.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return len(r.eips)
}

// CachedVectors returns previously cached embedding vectors keyed by chunk key.
func (r *Registry) CachedVectors() map[string]types.EIPVector {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.cache == nil || r.cache.Vectors == nil {
		return nil
	}

	result := make(map[string]types.EIPVector, len(r.cache.Vectors))
	for k, v := range r.cache.Vectors {
		result[k] = v
	}

	return result
}

// SaveVectors persists updated embedding vectors to the cache file.
func (r *Registry) SaveVectors(vectors map[string]types.EIPVector) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.cache == nil {
		return fmt.Errorf("no cache data available")
	}

	r.cache.Vectors = vectors

	return saveCache(r.cacheDir, r.cache)
}

// Statuses returns all unique statuses across all EIPs, sorted.
func (r *Registry) Statuses() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return uniqueSorted(r.eips, func(e types.EIP) string { return e.Status })
}

// Categories returns all unique categories across all EIPs, sorted.
func (r *Registry) Categories() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return uniqueSorted(r.eips, func(e types.EIP) string { return e.Category })
}

// Types returns all unique types across all EIPs, sorted.
func (r *Registry) Types() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return uniqueSorted(r.eips, func(e types.EIP) string { return e.Type })
}

// Refresh forces a re-fetch from GitHub and rebuilds the registry.
func (r *Registry) Refresh(ctx context.Context) error {
	f := newFetcher(r.log)

	latestSHA, err := f.latestCommitSHA(ctx)
	if err != nil {
		return fmt.Errorf("checking latest commit: %w", err)
	}

	eips, err := f.fetchAll(ctx)
	if err != nil {
		return fmt.Errorf("fetching EIPs: %w", err)
	}

	sort.Slice(eips, func(i, j int) bool {
		return eips[i].Number < eips[j].Number
	})

	// Preserve old vectors — they'll be revalidated during next index build.
	r.mu.RLock()
	var oldVectors map[string]types.EIPVector

	if r.cache != nil {
		oldVectors = r.cache.Vectors
	}

	r.mu.RUnlock()

	newCache := &cacheData{
		CommitSHA: latestSHA,
		FetchedAt: time.Now(),
		EIPs:      eips,
		Vectors:   oldVectors,
	}

	if err := saveCache(r.cacheDir, newCache); err != nil {
		r.log.WithError(err).Warn("Failed to save EIP cache")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.eips = eips
	r.byNumber = make(map[int]*types.EIP, len(eips))

	for i := range eips {
		r.byNumber[eips[i].Number] = &eips[i]
	}

	r.cache = newCache

	r.log.WithFields(logrus.Fields{
		"eip_count":  len(eips),
		"commit_sha": shortSHA(latestSHA),
	}).Info("EIP registry refreshed")

	return nil
}

func uniqueSorted(eips []types.EIP, field func(types.EIP) string) []string {
	set := make(map[string]struct{})
	for _, eip := range eips {
		if v := field(eip); v != "" {
			set[v] = struct{}{}
		}
	}

	result := make([]string, 0, len(set))
	for s := range set {
		result = append(result, s)
	}

	sort.Strings(result)

	return result
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}

	return sha
}

func loadCache(cacheDir string) (*cacheData, error) {
	data, err := os.ReadFile(filepath.Join(cacheDir, "eips.json"))
	if err != nil {
		return nil, err
	}

	var cache cacheData
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, fmt.Errorf("parsing cache: %w", err)
	}

	return &cache, nil
}

func saveCache(cacheDir string, cache *cacheData) error {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return fmt.Errorf("creating cache dir: %w", err)
	}

	data, err := json.Marshal(cache)
	if err != nil {
		return fmt.Errorf("marshaling cache: %w", err)
	}

	return os.WriteFile(filepath.Join(cacheDir, "eips.json"), data, 0o644)
}
