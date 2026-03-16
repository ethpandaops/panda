package eips

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/types"
)

type cacheData struct {
	CommitSHA string                     `json:"commit_sha"`
	FetchedAt time.Time                  `json:"fetched_at"`
	EIPs      []types.EIP                `json:"eips"`
	Vectors   map[string]types.EIPVector `json:"vectors,omitempty"`
}

// Registry manages a collection of parsed EIPs with disk caching.
type Registry struct {
	log      logrus.FieldLogger
	cacheDir string
	eips     []types.EIP
	byNumber map[int]*types.EIP
	cache    *cacheData
	mu       sync.RWMutex
}

// NewRegistry creates an EIP registry, fetching from GitHub if the
// cache is stale.
func NewRegistry(
	ctx context.Context,
	log logrus.FieldLogger,
	cacheDir string,
) (*Registry, error) {
	log = log.WithField("component", "eip_registry")

	if cacheDir == "" {
		userCache, err := os.UserCacheDir()
		if err != nil {
			return nil, fmt.Errorf("determining cache directory: %w", err)
		}

		cacheDir = filepath.Join(userCache, "ethpandaops-panda", "eips")
	}

	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating cache directory: %w", err)
	}

	f := newFetcher()

	latestSHA, err := f.latestCommitSHA(ctx)
	if err != nil {
		log.WithError(err).
			Warn("Failed to check latest EIP commit — trying cache")

		return loadFromCache(log, cacheDir)
	}

	cached, cacheErr := readCache(cacheDir)
	if cacheErr == nil && cached.CommitSHA == latestSHA {
		log.WithField("commit", latestSHA[:8]).
			Info("EIP cache is current")

		return registryFromCache(log, cacheDir, cached)
	}

	log.WithField("commit", latestSHA[:8]).
		Info("Fetching EIPs from GitHub")

	eipList, err := f.fetchAll(ctx)
	if err != nil {
		log.WithError(err).
			Warn("Failed to fetch EIPs — trying cache")

		return loadFromCache(log, cacheDir)
	}

	sort.Slice(eipList, func(i, j int) bool {
		return eipList[i].Number < eipList[j].Number
	})

	var oldVectors map[string]types.EIPVector
	if cached != nil {
		oldVectors = cached.Vectors
	}

	newCache := &cacheData{
		CommitSHA: latestSHA,
		FetchedAt: time.Now(),
		EIPs:      eipList,
		Vectors:   oldVectors,
	}

	if err := writeCache(cacheDir, newCache); err != nil {
		log.WithError(err).Warn("Failed to write EIP cache")
	}

	log.WithField("eip_count", len(eipList)).
		Info("EIP registry initialized")

	return buildRegistry(log, cacheDir, newCache), nil
}

// All returns a copy of all EIPs.
func (r *Registry) All() []types.EIP {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]types.EIP, len(r.eips))
	copy(out, r.eips)

	return out
}

// Count returns the number of EIPs in the registry.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return len(r.eips)
}

// CachedVectors returns a copy of the cached embedding vectors.
func (r *Registry) CachedVectors() map[string]types.EIPVector {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.cache == nil || len(r.cache.Vectors) == 0 {
		return nil
	}

	out := make(map[string]types.EIPVector, len(r.cache.Vectors))
	maps.Copy(out, r.cache.Vectors)

	return out
}

// SaveVectors persists updated embedding vectors to the cache file.
func (r *Registry) SaveVectors(vectors map[string]types.EIPVector) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.cache == nil {
		return fmt.Errorf("no cache loaded")
	}

	r.cache.Vectors = vectors

	return writeCache(r.cacheDir, r.cache)
}

// Statuses returns sorted unique status values across all EIPs.
func (r *Registry) Statuses() []string {
	return r.uniqueField(func(e types.EIP) string { return e.Status })
}

// Categories returns sorted unique category values across all EIPs.
func (r *Registry) Categories() []string {
	return r.uniqueField(func(e types.EIP) string { return e.Category })
}

// Types returns sorted unique type values across all EIPs.
func (r *Registry) Types() []string {
	return r.uniqueField(func(e types.EIP) string { return e.Type })
}

// Refresh re-fetches EIPs from GitHub, preserving existing vectors.
func (r *Registry) Refresh(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	f := newFetcher()

	latestSHA, err := f.latestCommitSHA(ctx)
	if err != nil {
		return fmt.Errorf("checking latest commit: %w", err)
	}

	if r.cache != nil && r.cache.CommitSHA == latestSHA {
		return nil
	}

	eipList, err := f.fetchAll(ctx)
	if err != nil {
		return fmt.Errorf("fetching EIPs: %w", err)
	}

	sort.Slice(eipList, func(i, j int) bool {
		return eipList[i].Number < eipList[j].Number
	})

	var oldVectors map[string]types.EIPVector
	if r.cache != nil {
		oldVectors = r.cache.Vectors
	}

	r.cache = &cacheData{
		CommitSHA: latestSHA,
		FetchedAt: time.Now(),
		EIPs:      eipList,
		Vectors:   oldVectors,
	}

	r.eips = eipList
	r.byNumber = make(map[int]*types.EIP, len(eipList))

	for i := range r.eips {
		r.byNumber[r.eips[i].Number] = &r.eips[i]
	}

	return writeCache(r.cacheDir, r.cache)
}

func (r *Registry) uniqueField(
	extract func(types.EIP) string,
) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	seen := make(map[string]struct{}, 16)

	for _, e := range r.eips {
		if v := extract(e); v != "" {
			seen[v] = struct{}{}
		}
	}

	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}

	sort.Strings(out)

	return out
}

func buildRegistry(
	log logrus.FieldLogger,
	cacheDir string,
	cache *cacheData,
) *Registry {
	byNumber := make(map[int]*types.EIP, len(cache.EIPs))
	eips := make([]types.EIP, len(cache.EIPs))
	copy(eips, cache.EIPs)

	for i := range eips {
		byNumber[eips[i].Number] = &eips[i]
	}

	return &Registry{
		log:      log,
		cacheDir: cacheDir,
		eips:     eips,
		byNumber: byNumber,
		cache:    cache,
	}
}

func registryFromCache(
	log logrus.FieldLogger,
	cacheDir string,
	cache *cacheData,
) (*Registry, error) {
	return buildRegistry(log, cacheDir, cache), nil
}

func loadFromCache(
	log logrus.FieldLogger,
	cacheDir string,
) (*Registry, error) {
	cached, err := readCache(cacheDir)
	if err != nil {
		return nil, fmt.Errorf("no cached EIPs available: %w", err)
	}

	log.WithField("eip_count", len(cached.EIPs)).
		Info("Loaded EIPs from cache")

	return buildRegistry(log, cacheDir, cached), nil
}

func cachePath(cacheDir string) string {
	return filepath.Join(cacheDir, "eips.json")
}

func readCache(cacheDir string) (*cacheData, error) {
	data, err := os.ReadFile(cachePath(cacheDir))
	if err != nil {
		return nil, err
	}

	var cache cacheData
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, fmt.Errorf("decoding cache: %w", err)
	}

	return &cache, nil
}

func writeCache(cacheDir string, cache *cacheData) error {
	data, err := json.Marshal(cache)
	if err != nil {
		return fmt.Errorf("encoding cache: %w", err)
	}

	return os.WriteFile(cachePath(cacheDir), data, 0o644)
}
