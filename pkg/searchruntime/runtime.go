package searchruntime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/mcp/pkg/config"
	"github.com/ethpandaops/mcp/pkg/embedding"
	"github.com/ethpandaops/mcp/pkg/extension"
	"github.com/ethpandaops/mcp/pkg/resource"
	"github.com/ethpandaops/mcp/runbooks"
)

type Runtime struct {
	ExampleIndex    *resource.ExampleIndex
	RunbookRegistry *runbooks.Registry
	RunbookIndex    *resource.RunbookIndex
	embedder        *embedding.Embedder
}

func Build(
	log logrus.FieldLogger,
	cfg config.SemanticSearchConfig,
	extensionRegistry *extension.Registry,
) (*Runtime, error) {
	modelPath, searched := resolveModelPath(cfg.ModelPath)
	if modelPath == "" {
		return nil, fmt.Errorf(
			"embedding model not found. looked in: %s. run 'make download-models' or 'make install'",
			strings.Join(searched, ", "),
		)
	}

	embedder, err := embedding.New(modelPath, cfg.GPULayers)
	if err != nil {
		return nil, fmt.Errorf("creating embedder: %w", err)
	}

	runtime := &Runtime{embedder: embedder}

	exampleIndex, err := resource.NewExampleIndex(log, embedder, resource.GetQueryExamples(extensionRegistry))
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

	return runtime, nil
}

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

func resolveModelPathWithExecutable(configuredPath, execDir string) (string, []string) {
	defaultModel := "models/MiniLM-L6-v2.Q8_0.gguf"
	containerModel := "/usr/share/mcp/MiniLM-L6-v2.Q8_0.gguf"

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
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, candidates
		}
	}

	return "", candidates
}

func executableDir() string {
	path, err := os.Executable()
	if err != nil || path == "" {
		return ""
	}

	return filepath.Dir(path)
}
