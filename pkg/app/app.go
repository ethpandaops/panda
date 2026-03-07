// Package app provides the shared application core used by both the MCP server and the CLI.
// It handles plugin initialization, proxy connection, sandbox setup, and semantic search indices.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/mcp/pkg/config"
	"github.com/ethpandaops/mcp/pkg/eips"
	"github.com/ethpandaops/mcp/pkg/embedding"
	"github.com/ethpandaops/mcp/pkg/plugin"
	"github.com/ethpandaops/mcp/pkg/proxy"
	"github.com/ethpandaops/mcp/pkg/resource"
	"github.com/ethpandaops/mcp/pkg/sandbox"
	"github.com/ethpandaops/mcp/runbooks"

	clickhouseplugin "github.com/ethpandaops/mcp/plugins/clickhouse"
	doraplugin "github.com/ethpandaops/mcp/plugins/dora"
	ethnodeplugin "github.com/ethpandaops/mcp/plugins/ethnode"
	lokiplugin "github.com/ethpandaops/mcp/plugins/loki"
	prometheusplugin "github.com/ethpandaops/mcp/plugins/prometheus"
)

// App contains the shared core components used by both the MCP server and CLI.
type App struct {
	log logrus.FieldLogger
	cfg *config.Config

	PluginRegistry  *plugin.Registry
	Sandbox         sandbox.Service
	ProxyClient     proxy.Client
	Cartographoor   resource.CartographoorClient
	ExampleIndex    *resource.ExampleIndex
	RunbookRegistry *runbooks.Registry
	RunbookIndex    *resource.RunbookIndex
	EIPRegistry     *eips.Registry
	EIPIndex        *resource.EIPIndex
	Embedder        *embedding.Embedder
}

// New creates a new App.
func New(log logrus.FieldLogger, cfg *config.Config) *App {
	return &App{
		log: log.WithField("component", "app"),
		cfg: cfg,
	}
}

// Config returns the application configuration.
func (a *App) Config() *config.Config {
	return a.cfg
}

// Build initializes all shared components in dependency order:
// plugin registry -> sandbox -> proxy -> plugins start -> cartographoor -> search indices.
func (a *App) Build(ctx context.Context) error {
	a.log.Info("Building application dependencies")

	// 1. Build and initialize plugin registry.
	pluginReg, err := a.buildPluginRegistry()
	if err != nil {
		return fmt.Errorf("building plugin registry: %w", err)
	}

	a.PluginRegistry = pluginReg

	// 2. Create and start sandbox service.
	sandboxSvc, err := sandbox.New(a.cfg.Sandbox, a.log)
	if err != nil {
		return fmt.Errorf("building sandbox: %w", err)
	}

	if err := sandboxSvc.Start(ctx); err != nil {
		return fmt.Errorf("starting sandbox: %w", err)
	}

	a.Sandbox = sandboxSvc
	a.log.WithField("backend", sandboxSvc.Name()).Info("Sandbox service started")

	// 3. Create and start proxy client.
	proxyClient := a.buildProxyClient()
	if err := proxyClient.Start(ctx); err != nil {
		a.stop(ctx)

		return fmt.Errorf("starting proxy client: %w", err)
	}

	a.ProxyClient = proxyClient
	a.log.WithField("url", proxyClient.URL()).Info("Proxy client connected")

	// 4. Inject proxy client into plugins and start all plugins.
	a.injectProxyClient()

	if err := a.PluginRegistry.StartAll(ctx); err != nil {
		a.stop(ctx)

		return fmt.Errorf("starting plugins: %w", err)
	}

	a.log.Info("All plugins started")

	// 5. Create and start cartographoor client.
	cartographoorClient := resource.NewCartographoorClient(a.log, resource.CartographoorConfig{
		URL:      resource.DefaultCartographoorURL,
		CacheTTL: resource.DefaultCacheTTL,
		Timeout:  resource.DefaultHTTPTimeout,
	})

	if err := cartographoorClient.Start(ctx); err != nil {
		a.stop(ctx)

		return fmt.Errorf("starting cartographoor client: %w", err)
	}

	a.Cartographoor = cartographoorClient
	a.log.Info("Cartographoor client started")

	// 6. Inject cartographoor client into plugins.
	a.injectCartographoorClient()

	// 7. Build semantic search indices.
	if err := a.buildSearchIndices(ctx); err != nil {
		a.stop(ctx)

		return fmt.Errorf("building search indices: %w", err)
	}

	return nil
}

// BuildLight initializes only the plugin registry and proxy client.
// Plugins are started (e.g., schema discovery) but sandbox, cartographoor,
// and semantic search indices are not created.
func (a *App) BuildLight(ctx context.Context) error {
	a.log.Info("Building lightweight application dependencies")

	pluginReg, err := a.buildPluginRegistry()
	if err != nil {
		return fmt.Errorf("building plugin registry: %w", err)
	}

	a.PluginRegistry = pluginReg

	proxyClient := a.buildProxyClient()
	if err := proxyClient.Start(ctx); err != nil {
		return fmt.Errorf("starting proxy client: %w", err)
	}

	a.ProxyClient = proxyClient
	a.log.WithField("url", proxyClient.URL()).Info("Proxy client connected")

	a.injectProxyClient()

	if err := a.PluginRegistry.StartAll(ctx); err != nil {
		a.stop(ctx)

		return fmt.Errorf("starting plugins: %w", err)
	}

	a.log.Info("All plugins started")

	return nil
}

// BuildSearchOnly initializes only the plugin registry and semantic search indices.
// No proxy, sandbox, or cartographoor. Used by CLI search commands.
func (a *App) BuildSearchOnly(ctx context.Context) error {
	a.log.Info("Building search-only application dependencies")

	pluginReg, err := a.buildPluginRegistry()
	if err != nil {
		return fmt.Errorf("building plugin registry: %w", err)
	}

	a.PluginRegistry = pluginReg

	if err := a.buildSearchIndices(ctx); err != nil {
		return fmt.Errorf("building search indices: %w", err)
	}

	return nil
}

// BuildWithSandbox initializes plugins, proxy, and sandbox — but skips
// cartographoor and semantic search indices. Used by the CLI execute command.
func (a *App) BuildWithSandbox(ctx context.Context) error {
	a.log.Info("Building application dependencies (with sandbox)")

	pluginReg, err := a.buildPluginRegistry()
	if err != nil {
		return fmt.Errorf("building plugin registry: %w", err)
	}

	a.PluginRegistry = pluginReg

	sandboxSvc, err := sandbox.New(a.cfg.Sandbox, a.log)
	if err != nil {
		return fmt.Errorf("building sandbox: %w", err)
	}

	if err := sandboxSvc.Start(ctx); err != nil {
		return fmt.Errorf("starting sandbox: %w", err)
	}

	a.Sandbox = sandboxSvc
	a.log.WithField("backend", sandboxSvc.Name()).Info("Sandbox service started")

	proxyClient := a.buildProxyClient()
	if err := proxyClient.Start(ctx); err != nil {
		a.stop(ctx)

		return fmt.Errorf("starting proxy client: %w", err)
	}

	a.ProxyClient = proxyClient
	a.log.WithField("url", proxyClient.URL()).Info("Proxy client connected")

	a.injectProxyClient()

	if err := a.PluginRegistry.StartAll(ctx); err != nil {
		a.stop(ctx)

		return fmt.Errorf("starting plugins: %w", err)
	}

	a.log.Info("All plugins started")

	return nil
}

// Stop cleans up all started components in reverse order.
func (a *App) Stop(ctx context.Context) error {
	a.stop(ctx)

	return nil
}

func (a *App) stop(ctx context.Context) {
	if a.ExampleIndex != nil {
		_ = a.ExampleIndex.Close()
	} else if a.Embedder != nil {
		_ = a.Embedder.Close()
	}

	if a.Cartographoor != nil {
		_ = a.Cartographoor.Stop()
	}

	if a.PluginRegistry != nil {
		a.PluginRegistry.StopAll(ctx)
	}

	if a.ProxyClient != nil {
		_ = a.ProxyClient.Stop(ctx)
	}

	if a.Sandbox != nil {
		_ = a.Sandbox.Stop(ctx)
	}
}

func (a *App) buildPluginRegistry() (*plugin.Registry, error) {
	reg := plugin.NewRegistry(a.log)

	// Register all compiled-in plugins.
	reg.Add(clickhouseplugin.New())
	reg.Add(doraplugin.New())
	reg.Add(ethnodeplugin.New())
	reg.Add(lokiplugin.New())
	reg.Add(prometheusplugin.New())

	// Initialize plugins that have config or are default-enabled.
	for _, name := range reg.All() {
		rawYAML, err := a.cfg.PluginConfigYAML(name)
		if err != nil {
			return nil, fmt.Errorf("getting config for plugin %q: %w", name, err)
		}

		if rawYAML == nil {
			// Check if plugin is default-enabled.
			p := reg.Get(name)
			if de, ok := p.(plugin.DefaultEnabled); ok && de.DefaultEnabled() {
				if err := reg.InitPlugin(name, nil); err != nil {
					if errors.Is(err, plugin.ErrNoValidConfig) {
						a.log.WithField("plugin", name).Debug("Default-enabled plugin has no valid config, skipping")

						continue
					}

					return nil, fmt.Errorf("initializing default-enabled plugin %q: %w", name, err)
				}

				continue
			}

			a.log.WithField("plugin", name).Debug("Plugin not configured, skipping")

			continue
		}

		if err := reg.InitPlugin(name, rawYAML); err != nil {
			// Skip if no valid config (e.g., env vars not set).
			if errors.Is(err, plugin.ErrNoValidConfig) {
				a.log.WithField("plugin", name).Debug("Plugin has no valid config entries, skipping")

				continue
			}

			return nil, fmt.Errorf("initializing plugin %q: %w", name, err)
		}
	}

	a.log.WithField("initialized_count", len(reg.Initialized())).Info("Plugin registry built")

	return reg, nil
}

func (a *App) buildProxyClient() proxy.Client {
	cfg := proxy.ClientConfig{
		URL: a.cfg.Proxy.URL,
	}

	if a.cfg.Proxy.Auth != nil {
		cfg.IssuerURL = a.cfg.Proxy.Auth.IssuerURL
		cfg.ClientID = a.cfg.Proxy.Auth.ClientID
	}

	return proxy.NewClient(a.log, cfg)
}

func (a *App) injectProxyClient() {
	for _, p := range a.PluginRegistry.Initialized() {
		if aware, ok := p.(plugin.ProxyAware); ok {
			aware.SetProxyClient(a.ProxyClient)
			a.log.WithField("plugin", p.Name()).Debug("Injected proxy client into plugin")
		}
	}
}

func (a *App) injectCartographoorClient() {
	for _, p := range a.PluginRegistry.Initialized() {
		if aware, ok := p.(plugin.CartographoorAware); ok {
			aware.SetCartographoorClient(a.Cartographoor)
			a.log.WithField("plugin", p.Name()).Debug("Injected cartographoor client into plugin")
		}
	}
}

// SandboxEnv builds credential-free environment variables for sandbox execution.
// Includes proxy URL, datasource info, and S3 bucket — but no credentials.
func (a *App) SandboxEnv() (map[string]string, error) {
	env, err := a.PluginRegistry.SandboxEnv()
	if err != nil {
		return nil, fmt.Errorf("collecting sandbox env: %w", err)
	}

	env["ETHPANDAOPS_PROXY_URL"] = a.ProxyClient.URL()

	if bucket := a.ProxyClient.S3Bucket(); bucket != "" {
		env["ETHPANDAOPS_S3_BUCKET"] = bucket
	}

	if prefix := a.ProxyClient.S3PublicURLPrefix(); prefix != "" {
		env["ETHPANDAOPS_S3_PUBLIC_URL_PREFIX"] = prefix
	}

	// Datasources are proxy-authoritative; override plugin-provided lists.
	delete(env, "ETHPANDAOPS_CLICKHOUSE_DATASOURCES")
	delete(env, "ETHPANDAOPS_PROMETHEUS_DATASOURCES")
	delete(env, "ETHPANDAOPS_LOKI_DATASOURCES")

	if ds := a.ProxyClient.ClickHouseDatasourceInfo(); len(ds) > 0 {
		data, _ := json.Marshal(ds)
		env["ETHPANDAOPS_CLICKHOUSE_DATASOURCES"] = string(data)
	}

	if ds := a.ProxyClient.PrometheusDatasourceInfo(); len(ds) > 0 {
		data, _ := json.Marshal(ds)
		env["ETHPANDAOPS_PROMETHEUS_DATASOURCES"] = string(data)
	}

	if ds := a.ProxyClient.LokiDatasourceInfo(); len(ds) > 0 {
		data, _ := json.Marshal(ds)
		env["ETHPANDAOPS_LOKI_DATASOURCES"] = string(data)
	}

	return env, nil
}

func (a *App) buildSearchIndices(ctx context.Context) error {
	cfg := a.cfg.SemanticSearch
	if cfg.ModelPath == "" {
		return fmt.Errorf("semantic_search.model_path is required")
	}

	if _, err := os.Stat(cfg.ModelPath); os.IsNotExist(err) {
		return fmt.Errorf("embedding model not found at %s (run 'make download-models' to fetch it)", cfg.ModelPath)
	}

	embedder, err := embedding.New(cfg.ModelPath, cfg.GPULayers)
	if err != nil {
		return fmt.Errorf("creating embedder: %w", err)
	}

	a.Embedder = embedder

	exampleIndex, err := resource.NewExampleIndex(a.log, embedder, resource.GetQueryExamples(a.PluginRegistry))
	if err != nil {
		return fmt.Errorf("building example index: %w", err)
	}

	a.ExampleIndex = exampleIndex
	a.log.Info("Semantic search example index built")

	runbookReg, err := runbooks.NewRegistry(a.log)
	if err != nil {
		return fmt.Errorf("creating runbook registry: %w", err)
	}

	a.RunbookRegistry = runbookReg

	if runbookReg.Count() == 0 {
		a.log.Warn("No runbooks found, runbook search will be disabled")

		return nil
	}

	runbookIndex, err := resource.NewRunbookIndex(a.log, embedder, runbookReg.All())
	if err != nil {
		return fmt.Errorf("building runbook index: %w", err)
	}

	a.RunbookIndex = runbookIndex
	a.log.Info("Semantic search runbook index built")

	// Build EIP index (non-fatal: depends on GitHub availability).
	eipReg, err := eips.NewRegistry(ctx, a.log, "")
	if err != nil {
		a.log.WithError(err).Warn("Failed to load EIPs, EIP search will be disabled")

		return nil
	}

	a.EIPRegistry = eipReg

	if eipReg.Count() > 0 {
		eipIndex, updatedVectors, err := resource.NewEIPIndex(a.log, embedder, eipReg.All(), eipReg.CachedVectors())
		if err != nil {
			a.log.WithError(err).Warn("Failed to build EIP index, EIP search will be disabled")

			return nil
		}

		a.EIPIndex = eipIndex

		if err := eipReg.SaveVectors(updatedVectors); err != nil {
			a.log.WithError(err).Warn("Failed to save EIP embedding vectors")
		}

		a.log.Info("Semantic search EIP index built")
	}

	return nil
}
