package searchruntime

import (
	"context"
	"fmt"

	"github.com/sirupsen/logrus"

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
// Returns nil if the proxy does not have embedding configured.
func Build(
	ctx context.Context,
	log logrus.FieldLogger,
	moduleRegistry *module.Registry,
	proxyService proxy.Service,
) (*Runtime, error) {
	if proxyService == nil {
		return nil, fmt.Errorf("proxy service is required for semantic search")
	}

	if !proxyService.EmbeddingAvailable() {
		return nil, fmt.Errorf("proxy embedding not available: ensure the proxy has embedding configured")
	}

	log.WithField("model", proxyService.EmbeddingModel()).
		Info("Using remote embedder via proxy")

	embedder := embedding.NewRemote(
		log,
		proxyService.URL(),
		func() string { return proxyService.RegisterToken("embedding") },
	)

	runtime := &Runtime{embedder: embedder}

	exampleIndex, err := resource.NewExampleIndex(log, embedder, resource.GetQueryExamples(moduleRegistry))
	if err != nil {
		_ = runtime.Close()
		return nil, fmt.Errorf("building example index: %w", err)
	}

	runtime.ExampleIndex = exampleIndex
	log.Info("Semantic search example index built")

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

	runbookIndex, err := resource.NewRunbookIndex(log, embedder, runbookReg.All())
	if err != nil {
		_ = runtime.Close()
		return nil, fmt.Errorf("building runbook index: %w", err)
	}

	runtime.RunbookIndex = runbookIndex
	log.Info("Semantic search runbook index built")

	// Build EIP index (non-fatal — gracefully disabled if GitHub unreachable).
	eipReg, err := eips.NewRegistry(ctx, log, "")
	if err != nil {
		log.WithError(err).Warn("Failed to initialize EIP registry — EIP search disabled")

		return runtime, nil
	}

	if eipReg.Count() == 0 {
		log.Warn("No EIPs found, EIP search will be disabled")

		return runtime, nil
	}

	eipIndex, updatedVectors, err := resource.NewEIPIndex(log, embedder, eipReg.All(), eipReg.CachedVectors())
	if err != nil {
		log.WithError(err).Warn("Failed to build EIP index — EIP search disabled")

		return runtime, nil
	}

	if err := eipReg.SaveVectors(updatedVectors); err != nil {
		log.WithError(err).Warn("Failed to save EIP vectors to cache")
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
