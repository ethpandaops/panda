package module

import (
	"context"
	"fmt"
	"maps"
	"sync"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/cartographoor"
	"github.com/ethpandaops/panda/pkg/proxy"
	"github.com/ethpandaops/panda/pkg/types"
)

// Registry tracks all compiled-in modules and which ones are
// initialized (have config and passed Init/Validate).
type Registry struct {
	log         logrus.FieldLogger
	mu          sync.RWMutex
	all         map[string]Module
	initialized []Module
}

// NewRegistry creates a new module registry.
func NewRegistry(log logrus.FieldLogger) *Registry {
	return &Registry{
		log:         log.WithField("component", "module_registry"),
		all:         make(map[string]Module, 4),
		initialized: make([]Module, 0, 4),
	}
}

// Add registers a compiled-in module by name.
// This does not initialize the module; call InitModule for that.
func (r *Registry) Add(ext Module) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.all[ext.Name()] = ext
	r.log.WithField("module", ext.Name()).Debug("Registered module")
}

// InitModule initializes a module with the given raw YAML config.
// It calls Init, ApplyDefaults, and Validate in sequence.
// Returns ErrNoValidConfig if the module has no valid configuration entries,
// which should be handled by the caller as a graceful skip.
func (r *Registry) InitModule(name string, rawConfig []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	ext, ok := r.all[name]
	if !ok {
		return fmt.Errorf("unknown module %q", name)
	}

	if err := ext.Init(rawConfig); err != nil {
		return fmt.Errorf("initializing module %q: %w", name, err)
	}

	ext.ApplyDefaults()

	if err := ext.Validate(); err != nil {
		return fmt.Errorf("validating module %q: %w", name, err)
	}

	r.initialized = append(r.initialized, ext)

	r.log.WithField("module", name).Info("Module initialized")

	return nil
}

// InitModuleFromDiscovery initializes a module from discovered datasources.
// The module must implement ProxyDiscoverable.
func (r *Registry) InitModuleFromDiscovery(name string, datasources []types.DatasourceInfo) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	ext, ok := r.all[name]
	if !ok {
		return fmt.Errorf("unknown module %q", name)
	}

	discoverable, ok := ext.(ProxyDiscoverable)
	if !ok {
		return fmt.Errorf("module %q does not implement ProxyDiscoverable", name)
	}

	if err := discoverable.InitFromDiscovery(datasources); err != nil {
		return fmt.Errorf("initializing module %q from discovery: %w", name, err)
	}

	ext.ApplyDefaults()

	if err := ext.Validate(); err != nil {
		return fmt.Errorf("validating module %q: %w", name, err)
	}

	r.initialized = append(r.initialized, ext)

	r.log.WithField("module", name).Info("Module initialized from proxy discovery")

	return nil
}

// Initialized returns all modules that passed Init/Validate.
func (r *Registry) Initialized() []Module {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]Module, len(r.initialized))
	copy(result, r.initialized)

	return result
}

// All returns the names of all compiled-in modules.
func (r *Registry) All() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.all))
	for name := range r.all {
		names = append(names, name)
	}

	return names
}

// Get returns a module by name, or nil if not found.
func (r *Registry) Get(name string) Module {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.all[name]
}

// IsInitialized reports whether the named module was initialized successfully.
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

// StartAll starts all initialized modules.
func (r *Registry) StartAll(ctx context.Context) error {
	r.mu.RLock()
	modules := make([]Module, len(r.initialized))
	copy(modules, r.initialized)
	r.mu.RUnlock()

	for _, ext := range modules {
		starter, ok := ext.(Starter)
		if !ok {
			continue
		}

		if err := starter.Start(ctx); err != nil {
			return fmt.Errorf("starting module %q: %w", ext.Name(), err)
		}

		r.log.WithField("module", ext.Name()).Info("Module started")
	}

	return nil
}

// StopAll stops all initialized modules.
func (r *Registry) StopAll(ctx context.Context) {
	r.mu.RLock()
	modules := make([]Module, len(r.initialized))
	copy(modules, r.initialized)
	r.mu.RUnlock()

	for _, ext := range modules {
		stopper, ok := ext.(Stopper)
		if !ok {
			continue
		}

		if err := stopper.Stop(ctx); err != nil {
			r.log.WithError(err).WithField("module", ext.Name()).Warn("Failed to stop module")
		}
	}
}

// InjectProxyAccess wires proxy-backed collaborators into initialized modules
// that declare the typed capability.
func (r *Registry) InjectProxyAccess(client proxy.ClickHouseSchemaAccess) {
	r.mu.RLock()
	modules := make([]Module, len(r.initialized))
	copy(modules, r.initialized)
	r.mu.RUnlock()

	for _, ext := range modules {
		aware, ok := ext.(ProxyAware)
		if !ok {
			continue
		}

		aware.SetProxyClient(client)
		r.log.WithField("module", ext.Name()).Debug("Injected proxy client into module")
	}
}

// InjectCartographoorClient wires cartographoor-backed collaborators into
// initialized modules that declare the typed capability.
func (r *Registry) InjectCartographoorClient(client cartographoor.CartographoorClient) {
	r.mu.RLock()
	modules := make([]Module, len(r.initialized))
	copy(modules, r.initialized)
	r.mu.RUnlock()

	for _, ext := range modules {
		aware, ok := ext.(CartographoorAware)
		if !ok {
			continue
		}

		aware.SetCartographoorClient(client)
		r.log.WithField("module", ext.Name()).Debug("Injected cartographoor client into module")
	}
}

// RegisterResources calls optional resource registration hooks on initialized
// modules so modules that expose no custom resources do not need no-op methods.
func (r *Registry) RegisterResources(log logrus.FieldLogger, reg ResourceRegistry) {
	r.mu.RLock()
	modules := make([]Module, len(r.initialized))
	copy(modules, r.initialized)
	r.mu.RUnlock()

	for _, ext := range modules {
		provider, ok := ext.(ResourceProvider)
		if !ok {
			continue
		}

		if err := provider.RegisterResources(log, reg); err != nil {
			log.WithError(err).WithField("module", ext.Name()).Warn("Failed to register module resources")
		}
	}
}

// SandboxEnv aggregates sandbox environment variables from all initialized modules.
func (r *Registry) SandboxEnv() (map[string]string, error) {
	r.mu.RLock()
	modules := make([]Module, len(r.initialized))
	copy(modules, r.initialized)
	r.mu.RUnlock()

	env := make(map[string]string, 8)

	for _, ext := range modules {
		provider, ok := ext.(SandboxEnvProvider)
		if !ok {
			continue
		}

		extEnv, err := provider.SandboxEnv()
		if err != nil {
			return nil, fmt.Errorf("getting sandbox env for module %q: %w", ext.Name(), err)
		}

		maps.Copy(env, extEnv)
	}

	return env, nil
}

// Examples aggregates query examples from all initialized modules.
func (r *Registry) Examples() map[string]types.ExampleCategory {
	r.mu.RLock()
	modules := make([]Module, len(r.initialized))
	copy(modules, r.initialized)
	r.mu.RUnlock()

	result := make(map[string]types.ExampleCategory, 16)

	for _, ext := range modules {
		provider, ok := ext.(ExamplesProvider)
		if !ok {
			continue
		}

		maps.Copy(result, provider.Examples())
	}

	return result
}

// PythonAPIDocs aggregates Python API docs from all initialized modules.
func (r *Registry) PythonAPIDocs() map[string]types.ModuleDoc {
	r.mu.RLock()
	modules := make([]Module, len(r.initialized))
	copy(modules, r.initialized)
	r.mu.RUnlock()

	result := make(map[string]types.ModuleDoc, 8)

	for _, ext := range modules {
		provider, ok := ext.(PythonAPIDocsProvider)
		if !ok {
			continue
		}

		maps.Copy(result, provider.PythonAPIDocs())
	}

	return result
}

// GettingStartedSnippets aggregates getting-started snippets from
// all initialized modules.
func (r *Registry) GettingStartedSnippets() string {
	r.mu.RLock()
	modules := make([]Module, len(r.initialized))
	copy(modules, r.initialized)
	r.mu.RUnlock()

	var snippets string
	for _, ext := range modules {
		provider, ok := ext.(GettingStartedSnippetProvider)
		if !ok {
			continue
		}

		snippet := provider.GettingStartedSnippet()
		if snippet != "" {
			snippets += snippet + "\n"
		}
	}

	return snippets
}
