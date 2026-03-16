package searchruntime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/config"
	"github.com/ethpandaops/panda/pkg/eips"
	"github.com/ethpandaops/panda/pkg/embedding"
	"github.com/ethpandaops/panda/pkg/module"
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
	embedder        *embedding.Embedder
}

// Build creates a new search runtime with example, runbook, and EIP indices.
func Build(
	ctx context.Context,
	log logrus.FieldLogger,
	cfg config.SemanticSearchConfig,
	moduleRegistry *module.Registry,
) (*Runtime, error) {
	modelPath, searched := resolveModelPath(cfg.ModelPath)
	if modelPath == "" {
		log.WithField("searched", strings.Join(searched, ", ")).
			Warn("Embedding model not found — semantic search disabled. Run 'make download-models' to enable it.")

		return nil, nil
	}

	embedder, err := embedding.New(modelPath)
	if err != nil {
		log.WithError(err).Warn("Failed to initialize embedder — semantic search disabled")

		return nil, nil
	}

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

func resolveModelPath(configuredPath string) (string, []string) {
	return resolveModelPathWithExecutable(configuredPath, executableDir())
}

// resolveModelPathWithExecutable searches for the ONNX model directory.
// The model directory must contain model.onnx and tokenizer.json.
func resolveModelPathWithExecutable(configuredPath, execDir string) (string, []string) {
	defaultModel := "models/all-MiniLM-L6-v2"
	containerModel := "/usr/share/panda/all-MiniLM-L6-v2"

	candidates := make([]string, 0, 6)
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}

		path = filepath.Clean(path)
		for _, candidate := range candidates {
			if candidate == path {
				return
			}
		}

		candidates = append(candidates, path)
	}

	if envPath := os.Getenv("ETHPANDAOPS_SEARCH_MODEL_PATH"); envPath != "" {
		add(envPath)
	}

	if configuredPath != "" {
		add(configuredPath)
		if !filepath.IsAbs(configuredPath) && execDir != "" {
			add(filepath.Join(execDir, configuredPath))
		}
	} else {
		add(defaultModel)
		if execDir != "" {
			add(filepath.Join(execDir, defaultModel))
		}
		add(containerModel)
	}

	for _, candidate := range candidates {
		if isModelDir(candidate) {
			return candidate, candidates
		}
	}

	return "", candidates
}

// isModelDir checks if a directory contains the required ONNX model files.
func isModelDir(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return false
	}

	for _, required := range []string{"model.onnx", "tokenizer.json"} {
		if _, err := os.Stat(filepath.Join(path, required)); err != nil {
			return false
		}
	}

	return true
}

func executableDir() string {
	path, err := os.Executable()
	if err != nil || path == "" {
		return ""
	}

	return filepath.Dir(path)
}
