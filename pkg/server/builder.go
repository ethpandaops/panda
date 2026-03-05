package server

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/mcp/pkg/auth"
	"github.com/ethpandaops/mcp/pkg/config"
	"github.com/ethpandaops/mcp/pkg/embedding"
	"github.com/ethpandaops/mcp/pkg/plugin"
	"github.com/ethpandaops/mcp/pkg/proxy"
	"github.com/ethpandaops/mcp/pkg/resource"
	"github.com/ethpandaops/mcp/pkg/sandbox"
	"github.com/ethpandaops/mcp/pkg/tool"
	"github.com/ethpandaops/mcp/runbooks"

	clickhouseplugin "github.com/ethpandaops/mcp/plugins/clickhouse"
	doraplugin "github.com/ethpandaops/mcp/plugins/dora"
	ethnodeplugin "github.com/ethpandaops/mcp/plugins/ethnode"
	lokiplugin "github.com/ethpandaops/mcp/plugins/loki"
	prometheusplugin "github.com/ethpandaops/mcp/plugins/prometheus"
)

// Dependencies contains all the services required to run the MCP server.
type Dependencies struct {
	Logger           logrus.FieldLogger
	Config           *config.Config
	ToolRegistry     tool.Registry
	ResourceRegistry resource.Registry
	Sandbox          sandbox.Service
	Auth             auth.SimpleService
}

// Builder constructs and wires all dependencies for the MCP server.
type Builder struct {
	log logrus.FieldLogger
	cfg *config.Config
}

// NewBuilder creates a new server builder.
func NewBuilder(log logrus.FieldLogger, cfg *config.Config) *Builder {
	return &Builder{
		log: log.WithField("component", "builder"),
		cfg: cfg,
	}
}

// Build constructs all dependencies and returns the server service.
func (b *Builder) Build(ctx context.Context) (Service, error) {
	b.log.Info("Building MCP server dependencies")

	// Build and initialize plugin registry.
	pluginReg, err := b.buildPluginRegistry()
	if err != nil {
		return nil, fmt.Errorf("building plugin registry: %w", err)
	}

	// Create sandbox service.
	sandboxSvc, err := b.buildSandbox()
	if err != nil {
		return nil, fmt.Errorf("building sandbox: %w", err)
	}

	// Start the sandbox service to initialize Docker client.
	if err := sandboxSvc.Start(ctx); err != nil {
		return nil, fmt.Errorf("starting sandbox: %w", err)
	}

	b.log.WithField("backend", sandboxSvc.Name()).Info("Sandbox service started")

	// Create and start proxy client.
	// The proxy server must be running separately. Client discovers datasources on start.
	proxyClient := b.buildProxy()
	if err := proxyClient.Start(ctx); err != nil {
		_ = sandboxSvc.Stop(ctx)

		return nil, fmt.Errorf("starting proxy client: %w", err)
	}

	b.log.WithField("url", proxyClient.URL()).Info("Proxy client connected")

	// Inject proxy client into plugins that need it (e.g., schema discovery).
	b.injectProxyClient(pluginReg, proxyClient)

	// Start all initialized plugins (e.g., schema discovery).
	if err := pluginReg.StartAll(ctx); err != nil {
		_ = proxyClient.Stop(ctx)
		_ = sandboxSvc.Stop(ctx)

		return nil, fmt.Errorf("starting plugins: %w", err)
	}

	b.log.Info("All plugins started")

	// Create cartographoor client for network discovery.
	cartographoorClient := b.buildCartographoor()

	// Start the cartographoor client to fetch initial network data.
	if err := cartographoorClient.Start(ctx); err != nil {
		_ = proxyClient.Stop(ctx)
		pluginReg.StopAll(ctx)
		_ = sandboxSvc.Stop(ctx)

		return nil, fmt.Errorf("starting cartographoor client: %w", err)
	}

	b.log.Info("Cartographoor client started")

	// Inject cartographoor client into plugins that need it.
	b.injectCartographoorClient(pluginReg, cartographoorClient)

	// Create auth service.
	authSvc, err := b.buildAuth()
	if err != nil {
		_ = cartographoorClient.Stop()
		_ = proxyClient.Stop(ctx)
		pluginReg.StopAll(ctx)
		_ = sandboxSvc.Stop(ctx)

		return nil, fmt.Errorf("building auth: %w", err)
	}

	// Start the auth service.
	if err := authSvc.Start(ctx); err != nil {
		_ = cartographoorClient.Stop()
		_ = proxyClient.Stop(ctx)
		pluginReg.StopAll(ctx)
		_ = sandboxSvc.Stop(ctx)

		return nil, fmt.Errorf("starting auth: %w", err)
	}

	if authSvc.Enabled() {
		b.log.Info("Auth service started")
	}

	// Create embedding model and example index for semantic search.
	exampleIndex, embedder, err := b.buildExampleIndex(pluginReg)
	if err != nil {
		_ = authSvc.Stop()
		_ = cartographoorClient.Stop()
		_ = proxyClient.Stop(ctx)
		pluginReg.StopAll(ctx)
		_ = sandboxSvc.Stop(ctx)

		return nil, fmt.Errorf("building example index: %w", err)
	}

	if exampleIndex != nil {
		b.log.Info("Semantic search example index built")
	}

	// Create runbook registry and index for semantic search.
	runbookReg, runbookIndex, err := b.buildRunbookIndex(embedder)
	if err != nil {
		if exampleIndex != nil {
			_ = exampleIndex.Close()
		} else if embedder != nil {
			_ = embedder.Close()
		}

		_ = authSvc.Stop()
		_ = cartographoorClient.Stop()
		_ = proxyClient.Stop(ctx)
		pluginReg.StopAll(ctx)
		_ = sandboxSvc.Stop(ctx)

		return nil, fmt.Errorf("building runbook index: %w", err)
	}

	if runbookIndex != nil {
		b.log.Info("Semantic search runbook index built")
	}

	// Create tool registry and register tools.
	toolReg := b.buildToolRegistry(sandboxSvc, exampleIndex, pluginReg, proxyClient, runbookReg, runbookIndex)

	// Create resource registry and register resources.
	resourceReg := b.buildResourceRegistry(cartographoorClient, pluginReg, toolReg, proxyClient)

	// Create and return the server service.
	return NewService(
		b.log,
		b.cfg.Server,
		b.cfg.Auth,
		toolReg,
		resourceReg,
		sandboxSvc,
		authSvc,
	), nil
}

// buildPluginRegistry creates the plugin registry, registers all compiled-in
// plugins, and initializes those with config.
func (b *Builder) buildPluginRegistry() (*plugin.Registry, error) {
	reg := plugin.NewRegistry(b.log)

	// Register all compiled-in plugins.
	reg.Add(clickhouseplugin.New())
	reg.Add(doraplugin.New())
	reg.Add(ethnodeplugin.New())
	reg.Add(lokiplugin.New())
	reg.Add(prometheusplugin.New())

	// Initialize plugins that have config or are default-enabled.
	for _, name := range reg.All() {
		rawYAML, err := b.cfg.PluginConfigYAML(name)
		if err != nil {
			return nil, fmt.Errorf("getting config for plugin %q: %w", name, err)
		}

		if rawYAML == nil {
			// Check if plugin is default-enabled.
			p := reg.Get(name)
			if de, ok := p.(plugin.DefaultEnabled); ok && de.DefaultEnabled() {
				// Initialize with empty config.
				if err := reg.InitPlugin(name, nil); err != nil {
					// Skip if no valid config (e.g., env vars not set).
					if errors.Is(err, plugin.ErrNoValidConfig) {
						b.log.WithField("plugin", name).Debug("Default-enabled plugin has no valid config, skipping")

						continue
					}

					return nil, fmt.Errorf("initializing default-enabled plugin %q: %w", name, err)
				}

				continue
			}

			b.log.WithField("plugin", name).Debug("Plugin not configured, skipping")

			continue
		}

		if err := reg.InitPlugin(name, rawYAML); err != nil {
			// Skip if no valid config (e.g., env vars not set).
			if errors.Is(err, plugin.ErrNoValidConfig) {
				b.log.WithField("plugin", name).Debug("Plugin has no valid config entries, skipping")

				continue
			}

			return nil, fmt.Errorf("initializing plugin %q: %w", name, err)
		}
	}

	b.log.WithField("initialized_count", len(reg.Initialized())).Info("Plugin registry built")

	return reg, nil
}

// buildSandbox creates the sandbox service.
func (b *Builder) buildSandbox() (sandbox.Service, error) {
	return sandbox.New(b.cfg.Sandbox, b.log)
}

// buildAuth creates the auth service.
func (b *Builder) buildAuth() (auth.SimpleService, error) {
	return auth.NewSimpleService(b.log, b.cfg.Auth, b.cfg.Server.BaseURL)
}

// buildProxy creates the proxy client.
// The proxy server must be running separately (via `mcp proxy` or K8s deployment).
// Datasources are discovered from the proxy's /datasources endpoint.
func (b *Builder) buildProxy() proxy.Client {
	cfg := proxy.ClientConfig{
		URL: b.cfg.Proxy.URL,
	}

	// Add auth config if provided.
	if b.cfg.Proxy.Auth != nil {
		cfg.IssuerURL = b.cfg.Proxy.Auth.IssuerURL
		cfg.ClientID = b.cfg.Proxy.Auth.ClientID
	}

	return proxy.NewClient(b.log, cfg)
}

// buildToolRegistry creates and populates the tool registry.
func (b *Builder) buildToolRegistry(
	sandboxSvc sandbox.Service,
	exampleIndex *resource.ExampleIndex,
	pluginReg *plugin.Registry,
	proxyClient proxy.Service,
	runbookReg *runbooks.Registry,
	runbookIndex *resource.RunbookIndex,
) tool.Registry {
	reg := tool.NewRegistry(b.log)

	// Register execute_python tool.
	reg.Register(tool.NewExecutePythonTool(b.log, sandboxSvc, b.cfg, pluginReg, proxyClient))

	// Register manage_session tool.
	reg.Register(tool.NewManageSessionTool(b.log, sandboxSvc))

	// Register search_examples tool (requires example index).
	if exampleIndex != nil {
		reg.Register(tool.NewSearchExamplesTool(b.log, exampleIndex, pluginReg))
	}

	// Register search_runbooks tool (requires runbook index).
	if runbookIndex != nil && runbookReg != nil {
		reg.Register(tool.NewSearchRunbooksTool(b.log, runbookIndex, runbookReg))
	}

	b.log.WithField("tool_count", len(reg.List())).Info("Tool registry built")

	return reg
}

// buildCartographoor creates the cartographoor client for network discovery.
func (b *Builder) buildCartographoor() resource.CartographoorClient {
	return resource.NewCartographoorClient(b.log, resource.CartographoorConfig{
		URL:      resource.DefaultCartographoorURL,
		CacheTTL: resource.DefaultCacheTTL,
		Timeout:  resource.DefaultHTTPTimeout,
	})
}

// injectProxyClient passes the proxy client to plugins that need it.
func (b *Builder) injectProxyClient(
	pluginReg *plugin.Registry,
	client proxy.Service,
) {
	for _, p := range pluginReg.Initialized() {
		if aware, ok := p.(plugin.ProxyAware); ok {
			aware.SetProxyClient(client)
			b.log.WithField("plugin", p.Name()).Debug("Injected proxy client into plugin")
		}
	}
}

// injectCartographoorClient passes the cartographoor client to plugins that need it.
func (b *Builder) injectCartographoorClient(
	pluginReg *plugin.Registry,
	client resource.CartographoorClient,
) {
	for _, p := range pluginReg.Initialized() {
		if aware, ok := p.(plugin.CartographoorAware); ok {
			aware.SetCartographoorClient(client)
			b.log.WithField("plugin", p.Name()).Debug("Injected cartographoor client into plugin")
		}
	}
}

// buildExampleIndex creates the semantic search index for examples
// and returns the shared embedder.
func (b *Builder) buildExampleIndex(pluginReg *plugin.Registry) (*resource.ExampleIndex, *embedding.Embedder, error) {
	cfg := b.cfg.SemanticSearch
	if cfg.ModelPath == "" {
		return nil, nil, fmt.Errorf("semantic_search.model_path is required")
	}

	// Check if model file exists.
	if _, err := os.Stat(cfg.ModelPath); os.IsNotExist(err) {
		return nil, nil, fmt.Errorf("embedding model not found at %s (run 'make download-models' to fetch it)", cfg.ModelPath)
	}

	embedder, err := embedding.New(cfg.ModelPath, cfg.GPULayers)
	if err != nil {
		return nil, nil, fmt.Errorf("creating embedder: %w", err)
	}

	index, err := resource.NewExampleIndex(b.log, embedder, resource.GetQueryExamples(pluginReg))
	if err != nil {
		_ = embedder.Close()

		return nil, nil, fmt.Errorf("building example index: %w", err)
	}

	return index, embedder, nil
}

// buildRunbookIndex creates the runbook registry and semantic search index.
// Returns nil for both if runbook loading fails or no runbooks are available.
func (b *Builder) buildRunbookIndex(embedder *embedding.Embedder) (*runbooks.Registry, *resource.RunbookIndex, error) {
	if embedder == nil {
		return nil, nil, fmt.Errorf("embedder is required for runbook search")
	}

	// Create runbook registry (loads all embedded runbooks).
	runbookReg, err := runbooks.NewRegistry(b.log)
	if err != nil {
		return nil, nil, fmt.Errorf("creating runbook registry: %w", err)
	}

	if runbookReg.Count() == 0 {
		b.log.Warn("No runbooks found, search_runbooks tool will be disabled")

		return nil, nil, nil
	}

	// Build the runbook search index.
	index, err := resource.NewRunbookIndex(b.log, embedder, runbookReg.All())
	if err != nil {
		return nil, nil, fmt.Errorf("building runbook index: %w", err)
	}

	return runbookReg, index, nil
}

// buildResourceRegistry creates and populates the resource registry.
func (b *Builder) buildResourceRegistry(
	cartographoorClient resource.CartographoorClient,
	pluginReg *plugin.Registry,
	toolReg tool.Registry,
	proxyClient proxy.Client,
) resource.Registry {
	reg := resource.NewRegistry(b.log)

	// Register datasources resources (from plugin registry and proxy client).
	resource.RegisterDatasourcesResources(b.log, reg, pluginReg, proxyClient)

	// Register examples resources (from plugin registry).
	resource.RegisterExamplesResources(b.log, reg, pluginReg)

	// Register networks resources.
	resource.RegisterNetworksResources(b.log, reg, cartographoorClient)

	// Register Python library API resources (from plugin registry).
	resource.RegisterAPIResources(b.log, reg, pluginReg)

	// Register getting-started resource.
	resource.RegisterGettingStartedResources(b.log, reg, toolReg, pluginReg)

	// Register plugin-specific resources (e.g., clickhouse://tables).
	for _, p := range pluginReg.Initialized() {
		if err := p.RegisterResources(b.log, reg); err != nil {
			b.log.WithError(err).WithField("plugin", p.Name()).Warn("Failed to register plugin resources")
		}
	}

	staticCount := len(reg.ListStatic())
	templateCount := len(reg.ListTemplates())

	b.log.WithFields(logrus.Fields{
		"static_count":   staticCount,
		"template_count": templateCount,
	}).Info("Resource registry built")

	return reg
}
