// Package app provides the shared application core used by both the MCP server and the CLI.
// It handles module initialization, proxy connection, sandbox setup, and semantic search indices.
package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/mcp/pkg/cartographoor"
	"github.com/ethpandaops/mcp/pkg/config"
	"github.com/ethpandaops/mcp/pkg/module"
	"github.com/ethpandaops/mcp/pkg/proxy"
	"github.com/ethpandaops/mcp/pkg/sandbox"

	clickhousemodule "github.com/ethpandaops/mcp/modules/clickhouse"
	doramodule "github.com/ethpandaops/mcp/modules/dora"
	ethnodemodule "github.com/ethpandaops/mcp/modules/ethnode"
	lokimodule "github.com/ethpandaops/mcp/modules/loki"
	prometheusmodule "github.com/ethpandaops/mcp/modules/prometheus"
)

// App contains the shared core components used by both the MCP server and CLI.
type App struct {
	log logrus.FieldLogger
	cfg *config.Config

	ModuleRegistry *module.Registry
	Sandbox        sandbox.Service
	ProxyClient    proxy.Client
	Cartographoor  cartographoor.CartographoorClient
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
// module registry -> sandbox -> proxy -> module startup -> cartographoor -> search indices.
func (a *App) Build(ctx context.Context) error {
	a.log.Info("Building application dependencies")

	// 1. Build and initialize module registry.
	moduleReg, err := a.buildModuleRegistry()
	if err != nil {
		return fmt.Errorf("building module registry: %w", err)
	}

	a.ModuleRegistry = moduleReg

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

	// 4. Inject proxy client into modules and start all modules.
	a.injectProxyClient()

	if err := a.ModuleRegistry.StartAll(ctx); err != nil {
		a.stop(ctx)

		return fmt.Errorf("starting modules: %w", err)
	}

	a.log.Info("All modules started")

	// 5. Create and start cartographoor client.
	cartographoorClient := cartographoor.NewCartographoorClient(a.log, cartographoor.CartographoorConfig{
		URL:      cartographoor.DefaultCartographoorURL,
		CacheTTL: cartographoor.DefaultCacheTTL,
		Timeout:  cartographoor.DefaultHTTPTimeout,
	})

	if err := cartographoorClient.Start(ctx); err != nil {
		a.stop(ctx)

		return fmt.Errorf("starting cartographoor client: %w", err)
	}

	a.Cartographoor = cartographoorClient
	a.log.Info("Cartographoor client started")

	// 6. Inject cartographoor client into modules.
	a.injectCartographoorClient()

	return nil
}

// Stop cleans up all started components in reverse order.
func (a *App) Stop(ctx context.Context) error {
	a.stop(ctx)

	return nil
}

func (a *App) stop(ctx context.Context) {
	if a.Cartographoor != nil {
		_ = a.Cartographoor.Stop()
	}

	if a.ModuleRegistry != nil {
		a.ModuleRegistry.StopAll(ctx)
	}

	if a.ProxyClient != nil {
		_ = a.ProxyClient.Stop(ctx)
	}

	if a.Sandbox != nil {
		_ = a.Sandbox.Stop(ctx)
	}
}

func (a *App) buildModuleRegistry() (*module.Registry, error) {
	reg := module.NewRegistry(a.log)

	// Register all compiled-in modules.
	reg.Add(clickhousemodule.New())
	reg.Add(doramodule.New())
	reg.Add(ethnodemodule.New())
	reg.Add(lokimodule.New())
	reg.Add(prometheusmodule.New())

	// Initialize modules that have config or are default-enabled.
	for _, name := range reg.All() {
		rawYAML, err := a.cfg.ModuleConfigYAML(name)
		if err != nil {
			return nil, fmt.Errorf("getting config for module %q: %w", name, err)
		}

		if rawYAML == nil {
			// Check if the module is default-enabled.
			ext := reg.Get(name)
			if de, ok := ext.(module.DefaultEnabled); ok && de.DefaultEnabled() {
				if err := reg.InitModule(name, nil); err != nil {
					if errors.Is(err, module.ErrNoValidConfig) {
						a.log.WithField("module", name).Debug("Default-enabled module has no valid config, skipping")

						continue
					}

					return nil, fmt.Errorf("initializing default-enabled module %q: %w", name, err)
				}

				continue
			}

			a.log.WithField("module", name).Debug("Module not configured, skipping")

			continue
		}

		if err := reg.InitModule(name, rawYAML); err != nil {
			// Skip if no valid config (e.g., env vars not set).
			if errors.Is(err, module.ErrNoValidConfig) {
				a.log.WithField("module", name).Debug("Module has no valid config entries, skipping")

				continue
			}

			return nil, fmt.Errorf("initializing module %q: %w", name, err)
		}
	}

	a.log.WithField("initialized_count", len(reg.Initialized())).Info("Module registry built")

	return reg, nil
}

func (a *App) buildProxyClient() proxy.Client {
	cfg := proxy.ClientConfig{
		URL: a.cfg.Proxy.URL,
	}

	if a.cfg.Proxy.Auth != nil {
		cfg.IssuerURL = a.cfg.Proxy.Auth.IssuerURL
		cfg.ClientID = a.cfg.Proxy.Auth.ClientID
		cfg.Resource = a.cfg.Proxy.URL
	}

	return proxy.NewClient(a.log, cfg)
}

func (a *App) injectProxyClient() {
	for _, ext := range a.ModuleRegistry.Initialized() {
		if aware, ok := ext.(module.ProxyAware); ok {
			aware.SetProxyClient(a.ProxyClient)
			a.log.WithField("module", ext.Name()).Debug("Injected proxy client into module")
		}
	}
}

func (a *App) injectCartographoorClient() {
	for _, ext := range a.ModuleRegistry.Initialized() {
		if aware, ok := ext.(module.CartographoorAware); ok {
			aware.SetCartographoorClient(a.Cartographoor)
			a.log.WithField("module", ext.Name()).Debug("Injected cartographoor client into module")
		}
	}
}
