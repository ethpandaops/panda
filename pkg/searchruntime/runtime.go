package searchruntime

import (
	"context"
	"fmt"
	"hash/fnv"
	"sort"
	"sync"
	"sync/atomic"
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

	// activated reports whether search has been brought online (indices built
	// and the background refresher started). activating is a single-flight guard
	// so concurrent discovery events don't trigger overlapping activation builds.
	activated  atomic.Bool
	activating atomic.Bool

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
	runtime.log = log
	runtime.moduleRegistry = moduleRegistry

	// The runbook registry is local embedded content — it never needs an
	// embedding provider — so build it unconditionally. This keeps the
	// runbooks:// ref readers (`panda read`) working even when semantic search
	// is unavailable.
	runbookReg, err := runbooks.NewRegistry(log)
	if err != nil {
		return nil, fmt.Errorf("creating runbook registry: %w", err)
	}

	runtime.RunbookRegistry = runbookReg

	// Without a primary (external) proxy, embedding can never become available:
	// serve the content registries for ref reads, but build no search index and
	// run no background poll.
	if proxyService == nil {
		log.Warn("Proxy service unavailable; semantic search disabled")

		return runtime, nil
	}

	if router, ok := proxyService.(proxy.Router); ok && router.Primary() == nil {
		log.Warn("No external proxy configured; semantic search disabled")

		return runtime, nil
	}

	runtime.proxyService = proxyService

	if cacheDir != "" {
		localCache, cacheErr := cache.NewFilesystem(cacheDir)
		if cacheErr != nil {
			log.WithError(cacheErr).Warn("Failed to create local embedding cache, continuing without")
		} else {
			runtime.localCache = localCache
			log.WithField("dir", cacheDir).Info("Local embedding cache enabled")
		}
	}

	// Fetch the EIP and consensus-specs registries (concurrent GitHub calls,
	// both non-fatal). They back the eips:// and consensus-specs:// ref readers
	// and feed the search indices once embedding is available.
	runtime.fetchExternalRegistries(ctx, specsCfg)

	// Create parked (not-ready) index wrappers so the background poll can build
	// and swap indices in — both at cold start (embedding not yet available) and
	// when the proxy later switches embedding model. A parked wrapper reports
	// "not ready" to searches until an index is swapped in.
	runtime.ExampleIndex = resource.NewRefreshableExampleIndex(nil)

	if runbookReg.Count() > 0 {
		runtime.RunbookIndex = resource.NewRefreshableRunbookIndex(nil)
	} else {
		log.Warn("No runbooks found, runbook search will be disabled")
	}

	if runtime.EIPRegistry != nil {
		runtime.EIPIndex = resource.NewRefreshableEIPIndex(nil)
	}

	if runtime.SpecsRegistry != nil {
		runtime.SpecsIndex = resource.NewRefreshableConsensusSpecIndex(nil)
	}

	// Build the indices now if embedding is already available so search is ready
	// the moment the server serves traffic. Otherwise leave them parked: OnDiscover
	// activates search on the next proxy discovery once embedding becomes available
	// (e.g. after `panda auth login` completes), with no restart required.
	runtime.activate(ctx)

	if !runtime.activated.Load() {
		log.Warn("Proxy embedding not available; semantic search will activate on the next proxy discovery once embedding is available")
	}

	return runtime, nil
}

// activate brings semantic search online: it builds every index for the
// embedding model the proxy is currently serving and, on success, hands ongoing
// refresh to the background timer. It is the single path that activates search —
// called synchronously at startup (embedding already available) and from
// OnDiscover (embedding became available after startup). It builds at most once;
// a failed build leaves it inactive so a later discovery retries.
func (r *Runtime) activate(ctx context.Context) {
	if r.activated.Load() {
		return
	}

	model, useV2 := resolveModel(ctx, r.proxyService)
	if model == "" {
		return // embedding not available yet
	}

	r.reindex(model, useV2)
	if r.builtModel == "" {
		return // build failed; a later discovery retries
	}

	r.activated.Store(true)
	r.startRefresh(exampleSignature(resource.GetQueryExamples(r.moduleRegistry)))
}

// OnDiscover is invoked after each successful proxy discovery. It activates
// semantic search the first time the proxy advertises an embedding model, then
// becomes a no-op (the background timer owns ongoing refresh). The build runs in
// its own goroutine so it never blocks the discovery path, guarded single-flight
// so overlapping discovery events cannot trigger concurrent builds.
func (r *Runtime) OnDiscover() {
	if r.proxyService == nil || r.activated.Load() {
		return
	}

	if !r.activating.CompareAndSwap(false, true) {
		return
	}

	r.wg.Add(1)

	go func() {
		defer r.wg.Done()
		defer r.activating.Store(false)

		r.activate(context.Background())
	}()
}

// fetchExternalRegistries populates the EIP and consensus-specs registries from
// GitHub. The two fetches are independent network calls, so they run
// concurrently. Both are non-fatal: a failure leaves the corresponding registry
// nil, which disables that ref scheme and its search but never blocks startup.
func (r *Runtime) fetchExternalRegistries(ctx context.Context, specsCfg config.ConsensusSpecsConfig) {
	var wg sync.WaitGroup

	wg.Add(2)

	go func() {
		defer wg.Done()

		r.log.Info("Fetching EIPs from GitHub for search index")

		eipReg, err := eips.NewRegistry(ctx, r.log, "")
		switch {
		case err != nil:
			r.log.WithError(err).Warn("Failed to initialize EIP registry — EIP search and refs disabled")
		case eipReg.Count() == 0:
			r.log.Warn("No EIPs found — EIP search and refs disabled")
		default:
			r.EIPRegistry = eipReg
		}
	}()

	go func() {
		defer wg.Done()

		r.log.Info("Fetching consensus specs from GitHub for search index")

		specsReg, err := consensusspecs.NewRegistry(ctx, r.log, specsCfg, "")
		switch {
		case err != nil:
			r.log.WithError(err).Warn("Failed to initialize consensus specs registry — specs search and refs disabled")
		case specsReg.SpecCount() == 0:
			r.log.Warn("No consensus specs found — specs search and refs disabled")
		default:
			r.SpecsRegistry = specsReg
		}
	}()

	wg.Wait()
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

	embedder := embedding.NewRemoteWithEndpoint(
		r.log,
		r.proxyService.URL(),
		tokenFn,
		r.proxyService.Invalidate,
		r.localCache,
		model,
		useV2,
	)

	// Log document-level progress so operators can watch an index build advance
	// in `panda server logs` (the corpus is large; a multi-minute build should
	// not look hung).
	embedder.OnProgress(func(completed, total int) {
		r.log.WithFields(logrus.Fields{"embedded": completed, "total": total}).
			Info("Embedding search index")
	})

	return embedder
}

// startRefresh runs the single background poll that (1) re-indexes the whole
// corpus when the proxy's served embedding model changes — the indices are keyed
// by model, so a change requires re-embedding under the new model — and otherwise
// (2) rebuilds the example index when the exposed example set changes. The model
// check takes priority: serving a stale-model index after the proxy switched
// would dot-product a new-model query against old-model vectors (garbage).
//
// It is started only once search has activated (an embedding model is built), so
// r.embedder is always set by the time the example-refresh path runs. Activation
// itself is event-driven (see OnDiscover), not polled.
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

// reindex builds every active search index under the given embedding model from
// its retained registry and swaps the fresh index in. It serves two cases: the
// first build when search activates (builtModel == ""), and a rebuild after the
// proxy's served model changed. It parks all indices not-ready first so no
// in-flight search dot-products a query against a stale- or mixed-model index. A
// brief not-ready window during the rebuild is the intended, correct behaviour —
// far better than silently mixing embedding spaces. The old embedder is dropped
// (not closed): it shares the local cache with the new one, which the runtime
// closes once at shutdown.
func (r *Runtime) reindex(model string, useV2 bool) {
	if r.builtModel == "" {
		r.log.WithFields(logrus.Fields{"model": model, "v2": useV2}).
			Info("Activating semantic search; building index corpus")
	} else {
		r.log.WithFields(logrus.Fields{"from": r.builtModel, "to": model, "v2": useV2}).
			Warn("Proxy embedding model changed; re-indexing search corpus")
	}

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

	// Track whether every index rebuilt. builtModel only advances on full
	// success: a partial failure (e.g. a transient proxy timeout on one index)
	// must leave the model-change guard unsatisfied so the next tick re-enters
	// reindex and retries — otherwise the failed index would stay parked
	// not-ready forever, since the guard would never fire again.
	ok := true

	if idx, err := resource.NewExampleIndex(r.log, embedder, resource.GetQueryExamples(r.moduleRegistry)); err != nil {
		r.log.WithError(err).Warn("Re-index: example index rebuild failed")

		ok = false
	} else {
		r.ExampleIndex.Swap(idx)
	}

	if r.RunbookIndex != nil && r.RunbookRegistry != nil {
		if idx, err := resource.NewRunbookIndex(r.log, embedder, r.RunbookRegistry.All()); err != nil {
			r.log.WithError(err).Warn("Re-index: runbook index rebuild failed")

			ok = false
		} else {
			r.RunbookIndex.Swap(idx)
		}
	}

	if r.EIPIndex != nil && r.EIPRegistry != nil {
		if idx, err := resource.NewEIPIndex(r.log, embedder, r.EIPRegistry.All()); err != nil {
			r.log.WithError(err).Warn("Re-index: EIP index rebuild failed")

			ok = false
		} else {
			r.EIPIndex.Swap(idx)
		}
	}

	if r.SpecsIndex != nil && r.SpecsRegistry != nil {
		if idx, err := resource.NewConsensusSpecIndex(r.log, embedder, r.SpecsRegistry.AllSpecs(), r.SpecsRegistry.AllConstants()); err != nil {
			r.log.WithError(err).Warn("Re-index: consensus spec index rebuild failed")

			ok = false
		} else {
			r.SpecsIndex.Swap(idx)
		}
	}

	if !ok {
		// Don't advance builtModel — the next tick will re-detect the model
		// change and retry. Any index that failed stays not-ready (never mixing
		// model spaces) until a retry rebuilds it.
		r.log.WithField("model", model).
			Warn("Re-index incomplete; some indices failed to rebuild — will retry on the next tick")

		return
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
