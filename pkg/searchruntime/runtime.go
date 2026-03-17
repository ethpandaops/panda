package searchruntime

import (
	"context"
	"fmt"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/cache"
	"github.com/ethpandaops/panda/pkg/eips"
	"github.com/ethpandaops/panda/pkg/embedding"
	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/proxy"
	"github.com/ethpandaops/panda/pkg/resource"
	"github.com/ethpandaops/panda/runbooks"
)

// Runtime holds the semantic search indices and embedder.
type Runtime struct {
	ExampleIndex    *resource.ExampleIndex
	RunbookRegistry *runbooks.Registry
	RunbookIndex    *resource.RunbookIndex
	EIPRegistry     *eips.Registry
	EIPIndex        *resource.EIPIndex
	embedder        embedding.Embedder
}

// Build creates a new search runtime with example, runbook, and EIP indices.
// Embedding is provided by the proxy's remote embedding service.
// cacheDir enables a local filesystem cache for embedding vectors when non-empty.
func Build(
	ctx context.Context,
	log logrus.FieldLogger,
	moduleRegistry *module.Registry,
	proxyService proxy.Service,
	cacheDir string,
) (*Runtime, error) {
	if proxyService == nil {
		return nil, fmt.Errorf("proxy service is required for semantic search")
	}

	if !proxyService.EmbeddingAvailable() {
		return nil, fmt.Errorf("proxy embedding not available: ensure the proxy has embedding configured")
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
		func() string { return proxyService.RegisterToken("embedding") },
		localCache,
		model,
	)

	runtime := &Runtime{embedder: embedder}

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

	runtime.ExampleIndex = exampleIndex

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

	// Build EIP index (non-fatal — gracefully disabled if GitHub unreachable).
	log.Info("Fetching EIPs from GitHub for search index")

	eipReg, err := eips.NewRegistry(ctx, log, "")
	if err != nil {
		log.WithError(err).Warn("Failed to initialize EIP registry — EIP search disabled")

		return runtime, nil
	}

	if eipReg.Count() == 0 {
		log.Warn("No EIPs found, EIP search will be disabled")

		return runtime, nil
	}

	log.WithField("eips", eipReg.Count()).Info("Building EIP search index")

	eipIndex, err := resource.NewEIPIndex(log, embedder, eipReg.All())
	if err != nil {
		log.WithError(err).Warn("Failed to build EIP index — EIP search disabled")

		return runtime, nil
	}

	runtime.EIPRegistry = eipReg
	runtime.EIPIndex = eipIndex
	log.Info("Semantic search EIP index built")

	return runtime, nil
}

// Close releases resources held by the runtime.
func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}

	if r.ExampleIndex != nil {
		return r.ExampleIndex.Close()
	}

	if r.embedder != nil {
		return r.embedder.Close()
	}

	return nil
}
