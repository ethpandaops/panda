package extension

import (
	"context"
	"fmt"
	"maps"
	"sync"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/mcp/pkg/types"
)

// Registry tracks all compiled-in extensions and which ones are
// initialized (have config and passed Init/Validate).
type Registry struct {
	log         logrus.FieldLogger
	mu          sync.RWMutex
	all         map[string]Extension
	initialized []Extension
}

// NewRegistry creates a new extension registry.
func NewRegistry(log logrus.FieldLogger) *Registry {
	return &Registry{
		log:         log.WithField("component", "extension_registry"),
		all:         make(map[string]Extension, 4),
		initialized: make([]Extension, 0, 4),
	}
}

// Add registers a compiled-in extension by name.
// This does not initialize the extension; call InitExtension for that.
func (r *Registry) Add(ext Extension) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.all[ext.Name()] = ext
	r.log.WithField("extension", ext.Name()).Debug("Registered extension")
}

// InitExtension initializes an extension with the given raw YAML config.
// It calls Init, ApplyDefaults, and Validate in sequence.
// Returns ErrNoValidConfig if the extension has no valid configuration entries,
// which should be handled by the caller as a graceful skip.
func (r *Registry) InitExtension(name string, rawConfig []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	ext, ok := r.all[name]
	if !ok {
		return fmt.Errorf("unknown extension %q", name)
	}

	if err := ext.Init(rawConfig); err != nil {
		return fmt.Errorf("initializing extension %q: %w", name, err)
	}

	ext.ApplyDefaults()

	if err := ext.Validate(); err != nil {
		return fmt.Errorf("validating extension %q: %w", name, err)
	}

	r.initialized = append(r.initialized, ext)

	r.log.WithField("extension", name).Info("Extension initialized")

	return nil
}

// Initialized returns all extensions that passed Init/Validate.
func (r *Registry) Initialized() []Extension {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]Extension, len(r.initialized))
	copy(result, r.initialized)

	return result
}

// All returns the names of all compiled-in extensions.
func (r *Registry) All() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.all))
	for name := range r.all {
		names = append(names, name)
	}

	return names
}

// Get returns an extension by name, or nil if not found.
func (r *Registry) Get(name string) Extension {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.all[name]
}

// IsInitialized reports whether the named extension was initialized successfully.
func (r *Registry) IsInitialized(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, ext := range r.initialized {
		if ext.Name() == name {
			return true
		}
	}

	return false
}

// StartAll starts all initialized extensions.
func (r *Registry) StartAll(ctx context.Context) error {
	r.mu.RLock()
	extensions := make([]Extension, len(r.initialized))
	copy(extensions, r.initialized)
	r.mu.RUnlock()

	for _, ext := range extensions {
		if err := ext.Start(ctx); err != nil {
			return fmt.Errorf("starting extension %q: %w", ext.Name(), err)
		}

		r.log.WithField("extension", ext.Name()).Info("Extension started")
	}

	return nil
}

// StopAll stops all initialized extensions.
func (r *Registry) StopAll(ctx context.Context) {
	r.mu.RLock()
	extensions := make([]Extension, len(r.initialized))
	copy(extensions, r.initialized)
	r.mu.RUnlock()

	for _, ext := range extensions {
		if err := ext.Stop(ctx); err != nil {
			r.log.WithError(err).WithField("extension", ext.Name()).Warn("Failed to stop extension")
		}
	}
}

// SandboxEnv aggregates credential-free sandbox environment variables
// from all initialized extensions. Credentials are never passed to sandbox
// containers - they connect via the credential proxy instead.
func (r *Registry) SandboxEnv() (map[string]string, error) {
	r.mu.RLock()
	extensions := make([]Extension, len(r.initialized))
	copy(extensions, r.initialized)
	r.mu.RUnlock()

	env := make(map[string]string, 8)

	for _, ext := range extensions {
		extEnv, err := ext.SandboxEnv()
		if err != nil {
			return nil, fmt.Errorf("getting sandbox env for extension %q: %w", ext.Name(), err)
		}

		maps.Copy(env, extEnv)
	}

	return env, nil
}

// DatasourceInfo aggregates datasource info from all initialized extensions.
func (r *Registry) DatasourceInfo() []types.DatasourceInfo {
	r.mu.RLock()
	extensions := make([]Extension, len(r.initialized))
	copy(extensions, r.initialized)
	r.mu.RUnlock()

	var infos []types.DatasourceInfo
	for _, ext := range extensions {
		infos = append(infos, ext.DatasourceInfo()...)
	}

	return infos
}

// Examples aggregates query examples from all initialized extensions.
func (r *Registry) Examples() map[string]types.ExampleCategory {
	r.mu.RLock()
	extensions := make([]Extension, len(r.initialized))
	copy(extensions, r.initialized)
	r.mu.RUnlock()

	result := make(map[string]types.ExampleCategory, 16)

	for _, ext := range extensions {
		maps.Copy(result, ext.Examples())
	}

	return result
}

// AllExamples aggregates query examples from ALL registered extensions,
// regardless of initialization status. Examples are static embedded data
// that don't require credentials.
func (r *Registry) AllExamples() map[string]types.ExampleCategory {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make(map[string]types.ExampleCategory, 16)

	for _, ext := range r.all {
		maps.Copy(result, ext.Examples())
	}

	return result
}

// PythonAPIDocs aggregates Python API docs from all initialized extensions.
func (r *Registry) PythonAPIDocs() map[string]types.ModuleDoc {
	r.mu.RLock()
	extensions := make([]Extension, len(r.initialized))
	copy(extensions, r.initialized)
	r.mu.RUnlock()

	result := make(map[string]types.ModuleDoc, 8)

	for _, ext := range extensions {
		maps.Copy(result, ext.PythonAPIDocs())
	}

	return result
}

// AllPythonAPIDocs aggregates Python API docs from ALL registered extensions,
// regardless of initialization status. API docs are static embedded data.
func (r *Registry) AllPythonAPIDocs() map[string]types.ModuleDoc {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make(map[string]types.ModuleDoc, 8)

	for _, ext := range r.all {
		maps.Copy(result, ext.PythonAPIDocs())
	}

	return result
}

// GettingStartedSnippets aggregates getting-started snippets from
// all initialized extensions.
func (r *Registry) GettingStartedSnippets() string {
	r.mu.RLock()
	extensions := make([]Extension, len(r.initialized))
	copy(extensions, r.initialized)
	r.mu.RUnlock()

	var snippets string
	for _, ext := range extensions {
		snippet := ext.GettingStartedSnippet()
		if snippet != "" {
			snippets += snippet + "\n"
		}
	}

	return snippets
}
