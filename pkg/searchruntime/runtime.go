package searchruntime

import (
	"context"
	"fmt"
	"hash/fnv"
	"sort"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/cache"
	"github.com/ethpandaops/panda/pkg/config"
	"github.com/ethpandaops/panda/pkg/consensusspecs"
	"github.com/ethpandaops/panda/pkg/eips"
	"github.com/ethpandaops/panda/pkg/embedding"
	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/proxy"
	"github.com/ethpandaops/panda/pkg/resource"
	"github.com/ethpandaops/panda/pkg/types"
	"github.com/ethpandaops/panda/runbooks"
)

// Runtime holds the semantic search indices and embedder.
type Runtime struct {
	ExampleIndex    *resource.RefreshableExampleIndex
	RunbookRegistry *runbooks.Registry
	RunbookIndex    *resource.RunbookIndex
	EIPRegistry     *eips.Registry
	EIPIndex        *resource.EIPIndex
	SpecsRegistry   *consensusspecs.Registry
	SpecsIndex      *resource.ConsensusSpecIndex
	embedder        embedding.Embedder

	stop chan struct{}
	wg   sync.WaitGroup
}

// exampleRefreshInterval is how often the example search index is rebuilt to
// track changes in the exposed example set (deployment scoping changes when
// the proxy's dataset declarations change). Rebuilds are skipped when the set
// is unchanged, so polling this often is cheap.
const exampleRefreshInterval = 5 * time.Minute

// Build creates a new search runtime with example, runbook, EIP, and
// consensus spec indices.
// Embedding is provided by the proxy's remote embedding service.
// cacheDir enables a local filesystem cache for embedding vectors when non-empty.
func Build(
	ctx context.Context,
	log logrus.FieldLogger,
	moduleRegistry *module.Registry,
	proxyService proxy.Service,
	cacheDir string,
	specsCfg config.ConsensusSpecsConfig,
) (*Runtime, error) {
	runtime := &Runtime{stop: make(chan struct{})}

	if proxyService == nil {
		log.Warn("Proxy service unavailable; semantic search disabled")

		return runtime, nil
	}

	if router, ok := proxyService.(proxy.Router); ok && router.Primary() == nil {
		log.Warn("No external proxy configured; semantic search disabled")

		return runtime, nil
	}

	if !proxyService.EmbeddingAvailable() {
		log.Warn("Proxy embedding not available; semantic search disabled")

		return runtime, nil
	}

	model := proxyService.EmbeddingModel()

	log.WithField("model", model).
		Info("Using remote embedder via proxy")

	var localCache cache.Cache

	if cacheDir != "" {
		var err error

		localCache, err = cache.NewFilesystem(cacheDir)
		if err != nil {
			log.WithError(err).Warn("Failed to create local embedding cache, continuing without")
		} else {
			log.WithField("dir", cacheDir).Info("Local embedding cache enabled")
		}
	}

	embedder := embedding.NewRemote(
		log,
		proxyService.URL(),
		func() string { return proxyService.RegisterToken() },
		localCache,
		model,
	)

	runtime.embedder = embedder

	examples := resource.GetQueryExamples(moduleRegistry)
	exampleCount := 0
	for _, cat := range examples {
		exampleCount += len(cat.Examples)
	}

	log.WithField("examples", exampleCount).Info("Building example search index")

	exampleIndex, err := resource.NewExampleIndex(log, embedder, examples)
	if err != nil {
		_ = runtime.Close()
		return nil, fmt.Errorf("building example index: %w", err)
	}

	runtime.ExampleIndex = resource.NewRefreshableExampleIndex(exampleIndex)
	runtime.startExampleRefresh(log, moduleRegistry, exampleSignature(examples))

	runbookReg, err := runbooks.NewRegistry(log)
	if err != nil {
		_ = runtime.Close()
		return nil, fmt.Errorf("creating runbook registry: %w", err)
	}

	runtime.RunbookRegistry = runbookReg

	if runbookReg.Count() == 0 {
		log.Warn("No runbooks found, runbook search will be disabled")
		return runtime, nil
	}

	log.WithField("runbooks", runbookReg.Count()).Info("Building runbook search index")

	runbookIndex, err := resource.NewRunbookIndex(log, embedder, runbookReg.All())
	if err != nil {
		_ = runtime.Close()
		return nil, fmt.Errorf("building runbook index: %w", err)
	}

	runtime.RunbookIndex = runbookIndex

	// Fetch EIP and consensus-specs registries concurrently. Both make
	// independent GitHub API calls, so parallelizing them reduces startup
	// latency. Both are non-fatal — gracefully disabled if GitHub is
	// unreachable.
	var (
		eipReg   *eips.Registry
		eipErr   error
		specsReg *consensusspecs.Registry
		specsErr error
		wg       sync.WaitGroup
	)

	wg.Add(2)

	go func() {
		defer wg.Done()
		log.Info("Fetching EIPs from GitHub for search index")
		eipReg, eipErr = eips.NewRegistry(ctx, log, "")
	}()

	go func() {
		defer wg.Done()
		log.Info("Fetching consensus specs from GitHub for search index")
		specsReg, specsErr = consensusspecs.NewRegistry(ctx, log, specsCfg, "")
	}()

	wg.Wait()

	// Build EIP search index from fetched registry.
	switch {
	case eipErr != nil:
		log.WithError(eipErr).Warn("Failed to initialize EIP registry — EIP search disabled")
	case eipReg.Count() == 0:
		log.Warn("No EIPs found, EIP search will be disabled")
	default:
		log.WithField("eips", eipReg.Count()).Info("Building EIP search index")

		eipIndex, indexErr := resource.NewEIPIndex(log, embedder, eipReg.All())
		if indexErr != nil {
			log.WithError(indexErr).Warn("Failed to build EIP index — EIP search disabled")
		} else {
			runtime.EIPRegistry = eipReg
			runtime.EIPIndex = eipIndex
			log.Info("Semantic search EIP index built")
		}
	}

	// Build consensus specs search index from fetched registry.
	switch {
	case specsErr != nil:
		log.WithError(specsErr).Warn("Failed to initialize consensus specs registry — specs search disabled")
	case specsReg.SpecCount() == 0:
		log.Warn("No consensus specs found, specs search will be disabled")
	default:
		log.WithFields(logrus.Fields{
			"specs":     specsReg.SpecCount(),
			"constants": specsReg.ConstantCount(),
		}).Info("Building consensus specs search index")

		specsIndex, indexErr := resource.NewConsensusSpecIndex(log, embedder, specsReg.AllSpecs(), specsReg.AllConstants())
		if indexErr != nil {
			log.WithError(indexErr).Warn("Failed to build consensus specs index — specs search disabled")
		} else {
			runtime.SpecsRegistry = specsReg
			runtime.SpecsIndex = specsIndex
			log.Info("Semantic search consensus specs index built")
		}
	}

	return runtime, nil
}

// Close stops the background refresher and releases the shared embedder.
func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}

	if r.stop != nil {
		select {
		case <-r.stop:
		default:
			close(r.stop)
		}
	}

	r.wg.Wait()

	if r.embedder != nil {
		return r.embedder.Close()
	}

	return nil
}

// startExampleRefresh rebuilds the example search index when the exposed
// example set changes (e.g. the proxy's dataset declarations changed scoping).
// Rebuilds are skipped while the set is unchanged, so the poll is cheap.
func (r *Runtime) startExampleRefresh(log logrus.FieldLogger, moduleRegistry *module.Registry, initialSig uint64) {
	r.wg.Add(1)

	go func() {
		defer r.wg.Done()

		ticker := time.NewTicker(exampleRefreshInterval)
		defer ticker.Stop()

		lastSig := initialSig

		for {
			select {
			case <-r.stop:
				return
			case <-ticker.C:
				examples := resource.GetQueryExamples(moduleRegistry)

				sig := exampleSignature(examples)
				if sig == lastSig {
					continue
				}

				index, err := resource.NewExampleIndex(log, r.embedder, examples)
				if err != nil {
					log.WithError(err).Warn("Example index refresh failed; keeping previous index")

					continue
				}

				r.ExampleIndex.Swap(index)
				lastSig = sig

				log.Info("Example search index refreshed after example set change")
			}
		}
	}()
}

// exampleSignature is a cheap fingerprint of the example set (category, name and
// target of every example). It changes whenever an example is added, removed, or
// re-targeted — the cases that warrant rebuilding the index.
func exampleSignature(categories map[string]types.ExampleCategory) uint64 {
	keys := make([]string, 0, len(categories))
	for key := range categories {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	h := fnv.New64a()

	for _, key := range keys {
		entries := make([]string, 0, len(categories[key].Examples))
		for _, ex := range categories[key].Examples {
			entries = append(entries, ex.Name+"\x00"+ex.Target)
		}

		sort.Strings(entries)

		_, _ = h.Write([]byte(key))

		for _, e := range entries {
			_, _ = h.Write([]byte{0})
			_, _ = h.Write([]byte(e))
		}
	}

	return h.Sum64()
}
