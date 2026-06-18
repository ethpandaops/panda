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
	RunbookIndex    *resource.RefreshableRunbookIndex
	EIPRegistry     *eips.Registry
	EIPIndex        *resource.RefreshableEIPIndex
	SpecsRegistry   *consensusspecs.Registry
	SpecsIndex      *resource.RefreshableConsensusSpecIndex
	embedder        embedding.Embedder

	// Retained so the background refresher can rebuild every index under a new
	// embedding model when the proxy's served model changes (the indices are
	// content-addressed by model, so a swap requires a full re-embed).
	log            logrus.FieldLogger
	moduleRegistry *module.Registry
	proxyService   proxy.Service
	localCache     cache.Cache
	builtModel     string

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

	// Prefer the versioned /v2/embedding route (fp32 @ a fixed dimensionality,
	// model advertised per response). Fall back to the legacy /embed routes when
	// the proxy does not expose v2, so a new server still works against an older
	// or self-hosted proxy. The model is whatever the chosen route serves: v2's
	// advertised model, or v1's configured model.
	model, useV2 := resolveModel(ctx, proxyService)

	log.WithFields(logrus.Fields{"model": model, "v2": useV2}).
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

	runtime.log = log
	runtime.moduleRegistry = moduleRegistry
	runtime.proxyService = proxyService
	runtime.localCache = localCache
	runtime.builtModel = model

	embedder := runtime.newEmbedder(model, useV2)
	runtime.embedder = embedder

	// Log document-level embedding progress so operators can watch the index
	// build advance in `panda server logs`. The embedder reports documents as
	// it works, attributed to whichever stage is currently building.
	currentStage := ""
	setStage := func(stage string) { currentStage = stage }

	embedder.OnProgress(func(completed, total int) {
		log.WithFields(logrus.Fields{
			"stage":    currentStage,
			"embedded": completed,
			"total":    total,
		}).Info("Embedding search index")
	})

	setStage("examples")

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
	initialSig := exampleSignature(examples)

	runbookReg, err := runbooks.NewRegistry(log)
	if err != nil {
		_ = runtime.Close()
		return nil, fmt.Errorf("creating runbook registry: %w", err)
	}

	runtime.RunbookRegistry = runbookReg

	if runbookReg.Count() == 0 {
		log.Warn("No runbooks found, runbook search will be disabled")
		runtime.startRefresh(initialSig)

		return runtime, nil
	}

	setStage("runbooks")
	log.WithField("runbooks", runbookReg.Count()).Info("Building runbook search index")

	runbookIndex, err := resource.NewRunbookIndex(log, embedder, runbookReg.All())
	if err != nil {
		_ = runtime.Close()
		return nil, fmt.Errorf("building runbook index: %w", err)
	}

	runtime.RunbookIndex = resource.NewRefreshableRunbookIndex(runbookIndex)

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
		setStage("EIPs")
		log.WithField("eips", eipReg.Count()).Info("Building EIP search index")

		eipIndex, indexErr := resource.NewEIPIndex(log, embedder, eipReg.All())
		if indexErr != nil {
			log.WithError(indexErr).Warn("Failed to build EIP index — EIP search disabled")
		} else {
			runtime.EIPRegistry = eipReg
			runtime.EIPIndex = resource.NewRefreshableEIPIndex(eipIndex)
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

		setStage("consensus specs")

		specsIndex, indexErr := resource.NewConsensusSpecIndex(log, embedder, specsReg.AllSpecs(), specsReg.AllConstants())
		if indexErr != nil {
			log.WithError(indexErr).Warn("Failed to build consensus specs index — specs search disabled")
		} else {
			runtime.SpecsRegistry = specsReg
			runtime.SpecsIndex = resource.NewRefreshableConsensusSpecIndex(specsIndex)
			log.Info("Semantic search consensus specs index built")
		}
	}

	runtime.startRefresh(initialSig)

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

// resolveModel returns the embedding model the server should use and whether it
// is reached via the versioned /v2/embedding route. It probes for v2 and falls
// back to the proxy's advertised v1 model. This is the single source of truth
// for "which model is the proxy serving right now" — used at startup and by the
// background refresher to detect a model change.
func resolveModel(ctx context.Context, proxyService proxy.Service) (string, bool) {
	tokenFn := func() string { return proxyService.RegisterToken() }

	if v2Model, ok := embedding.ProbeV2(ctx, proxyService.URL(), tokenFn); ok && v2Model != "" {
		return v2Model, true
	}

	return proxyService.EmbeddingModel(), false
}

// newEmbedder builds a proxy-backed embedder for the given model and route,
// reusing the runtime's shared local cache.
func (r *Runtime) newEmbedder(model string, useV2 bool) *embedding.RemoteEmbedder {
	tokenFn := func() string { return r.proxyService.RegisterToken() }

	return embedding.NewRemoteWithEndpoint(
		r.log,
		r.proxyService.URL(),
		tokenFn,
		r.proxyService.Invalidate,
		r.localCache,
		model,
		useV2,
	)
}

// startRefresh runs the single background poll that (1) re-indexes the whole
// corpus when the proxy's served embedding model changes — the indices are keyed
// by model, so a change requires re-embedding under the new model — and otherwise
// (2) rebuilds the example index when the exposed example set changes. The model
// check takes priority: serving a stale-model index after the proxy switched
// would dot-product a new-model query against old-model vectors (garbage).
func (r *Runtime) startRefresh(initialSig uint64) {
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
				model, useV2 := resolveModel(context.Background(), r.proxyService)
				if model != "" && model != r.builtModel {
					r.reindex(model, useV2)
					lastSig = exampleSignature(resource.GetQueryExamples(r.moduleRegistry))

					continue
				}

				examples := resource.GetQueryExamples(r.moduleRegistry)

				sig := exampleSignature(examples)
				if sig == lastSig {
					continue
				}

				index, err := resource.NewExampleIndex(r.log, r.embedder, examples)
				if err != nil {
					r.log.WithError(err).Warn("Example index refresh failed; keeping previous index")

					continue
				}

				r.ExampleIndex.Swap(index)
				lastSig = sig

				r.log.Info("Example search index refreshed after example set change")
			}
		}
	}()
}

// reindex rebuilds every active search index under a new embedding model after
// the proxy's served model changed. It parks all indices not-ready first so no
// in-flight search dot-products a new-model query against an old-model index,
// rebuilds each from its retained registry, then swaps the fresh index in. A
// brief "search reindexing" window during the rebuild is the intended, correct
// behaviour — far better than silently mixing embedding spaces. The old embedder
// is dropped (not closed): it shares the local cache with the new one, which the
// runtime closes once at shutdown.
func (r *Runtime) reindex(model string, useV2 bool) {
	r.log.WithFields(logrus.Fields{"from": r.builtModel, "to": model, "v2": useV2}).
		Warn("Proxy embedding model changed; re-indexing search corpus")

	embedder := r.newEmbedder(model, useV2)

	// Park everything not-ready up front so search never mixes model spaces.
	r.ExampleIndex.Swap(nil)

	if r.RunbookIndex != nil {
		r.RunbookIndex.Swap(nil)
	}

	if r.EIPIndex != nil {
		r.EIPIndex.Swap(nil)
	}

	if r.SpecsIndex != nil {
		r.SpecsIndex.Swap(nil)
	}

	if idx, err := resource.NewExampleIndex(r.log, embedder, resource.GetQueryExamples(r.moduleRegistry)); err != nil {
		r.log.WithError(err).Warn("Re-index: example index rebuild failed")
	} else {
		r.ExampleIndex.Swap(idx)
	}

	if r.RunbookIndex != nil && r.RunbookRegistry != nil {
		if idx, err := resource.NewRunbookIndex(r.log, embedder, r.RunbookRegistry.All()); err != nil {
			r.log.WithError(err).Warn("Re-index: runbook index rebuild failed")
		} else {
			r.RunbookIndex.Swap(idx)
		}
	}

	if r.EIPIndex != nil && r.EIPRegistry != nil {
		if idx, err := resource.NewEIPIndex(r.log, embedder, r.EIPRegistry.All()); err != nil {
			r.log.WithError(err).Warn("Re-index: EIP index rebuild failed")
		} else {
			r.EIPIndex.Swap(idx)
		}
	}

	if r.SpecsIndex != nil && r.SpecsRegistry != nil {
		if idx, err := resource.NewConsensusSpecIndex(r.log, embedder, r.SpecsRegistry.AllSpecs(), r.SpecsRegistry.AllConstants()); err != nil {
			r.log.WithError(err).Warn("Re-index: consensus spec index rebuild failed")
		} else {
			r.SpecsIndex.Swap(idx)
		}
	}

	r.embedder = embedder
	r.builtModel = model

	r.log.WithField("model", model).Info("Re-index complete")
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
