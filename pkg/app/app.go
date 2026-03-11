// Package app provides the shared application core used by both the MCP server and the CLI.
// It handles module initialization, proxy connection, sandbox setup, and semantic search indices.
package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/cartographoor"
	"github.com/ethpandaops/panda/pkg/config"
	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/proxy"
	"github.com/ethpandaops/panda/pkg/sandbox"
	"github.com/ethpandaops/panda/pkg/types"

	clickhousemodule "github.com/ethpandaops/panda/modules/clickhouse"
	doramodule "github.com/ethpandaops/panda/modules/dora"
	ethnodemodule "github.com/ethpandaops/panda/modules/ethnode"
	lokimodule "github.com/ethpandaops/panda/modules/loki"
	prometheusmodule "github.com/ethpandaops/panda/modules/prometheus"
)

// App contains the shared core components used by both the MCP server and CLI.
type App struct {
	log logrus.FieldLogger
	cfg *config.Config

	ModuleRegistry *module.Registry
	Sandbox        sandbox.Service
	ProxyClient    proxy.Client
	Cartographoor  cartographoor.CartographoorClient

	moduleRegistryBuilder func() (*module.Registry, error)
	sandboxBuilder        func() (sandbox.Service, error)
	proxyClientBuilder    func() proxy.Client
	cartographoorBuilder  func() cartographoor.CartographoorClient
}

// New creates a new App.
func New(log logrus.FieldLogger, cfg *config.Config) *App {
	app := &App{
		log: log.WithField("component", "app"),
		cfg: cfg,
	}

	app.moduleRegistryBuilder = app.buildModuleRegistry
	app.sandboxBuilder = func() (sandbox.Service, error) {
		return sandbox.New(app.cfg.Sandbox, app.log)
	}
	app.proxyClientBuilder = app.buildProxyClient
	app.cartographoorBuilder = app.newCartographoorClient

	return app
}

// Config returns the application configuration.
func (a *App) Config() *config.Config {
	return a.cfg
}

type buildOptions struct {
	withSandbox       bool
	withCartographoor bool
}

// Build initializes all shared components in dependency order:
// register modules -> sandbox -> proxy -> init modules -> module startup -> cartographoor.
func (a *App) Build(ctx context.Context) error {
	return a.build(ctx, buildOptions{
		withSandbox:       true,
		withCartographoor: true,
	})
}

// BuildLight initializes only the module registry and proxy client.
func (a *App) BuildLight(ctx context.Context) error {
	return a.build(ctx, buildOptions{})
}

// BuildWithSandbox initializes modules, proxy, and sandbox, but skips cartographoor.
func (a *App) BuildWithSandbox(ctx context.Context) error {
	return a.build(ctx, buildOptions{withSandbox: true})
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

func (a *App) build(ctx context.Context, opts buildOptions) error {
	a.log.Info("Building application dependencies")

	moduleReg, err := a.moduleRegistryBuilder()
	if err != nil {
		return fmt.Errorf("building module registry: %w", err)
	}

	a.ModuleRegistry = moduleReg

	if opts.withSandbox {
		sandboxSvc, err := a.sandboxBuilder()
		if err != nil {
			return fmt.Errorf("building sandbox: %w", err)
		}

		if err := sandboxSvc.Start(ctx); err != nil {
			return fmt.Errorf("starting sandbox: %w", err)
		}

		a.Sandbox = sandboxSvc
		a.log.WithField("backend", sandboxSvc.Name()).Info("Sandbox service started")
	}

	proxyClient := a.proxyClientBuilder()
	if err := proxyClient.Start(ctx); err != nil {
		a.stop(ctx)

		return fmt.Errorf("starting proxy client: %w", err)
	}

	a.ProxyClient = proxyClient
	a.log.WithField("url", proxyClient.URL()).Info("Proxy client connected")

	if err := a.initModules(proxyClient); err != nil {
		a.stop(ctx)

		return fmt.Errorf("initializing modules: %w", err)
	}

	a.injectProxyClient()

	if err := a.ModuleRegistry.StartAll(ctx); err != nil {
		a.stop(ctx)

		return fmt.Errorf("starting modules: %w", err)
	}

	a.log.Info("All modules started")

	if !opts.withCartographoor {
		return nil
	}

	cartographoorClient := a.cartographoorBuilder()
	if err := cartographoorClient.Start(ctx); err != nil {
		a.stop(ctx)

		return fmt.Errorf("starting cartographoor client: %w", err)
	}

	a.Cartographoor = cartographoorClient
	a.log.Info("Cartographoor client started")
	a.injectCartographoorClient()

	return nil
}

// buildModuleRegistry creates a module registry and registers all compiled-in
// modules without initializing them.
func (a *App) buildModuleRegistry() (*module.Registry, error) {
	reg := module.NewRegistry(a.log)

	reg.Add(clickhousemodule.New())
	reg.Add(doramodule.New())
	reg.Add(ethnodemodule.New())
	reg.Add(lokimodule.New())
	reg.Add(prometheusmodule.New())

	return reg, nil
}

// initModules initializes all registered modules.
func (a *App) initModules(proxyClient proxy.Client) error {
	reg := a.ModuleRegistry

	// Collect discovered datasources.
	var discovered []types.DatasourceInfo
	discovered = append(discovered, proxyClient.ClickHouseDatasourceInfo()...)
	discovered = append(discovered, proxyClient.PrometheusDatasourceInfo()...)
	discovered = append(discovered, proxyClient.LokiDatasourceInfo()...)

	if proxyClient.EthNodeAvailable() {
		discovered = append(discovered, types.DatasourceInfo{
			Type: "ethnode",
			Name: "ethnode",
		})
	}

	for _, name := range reg.All() {
		// Try proxy discovery for modules that support it.
		if len(discovered) > 0 {
			if err := reg.InitModuleFromDiscovery(name, discovered); err == nil {
				continue
			} else if !errors.Is(err, module.ErrNoValidConfig) &&
				!strings.Contains(err.Error(), "does not implement ProxyDiscoverable") {
				return fmt.Errorf("initializing module %q from discovery: %w", name, err)
			}
		}

		// DefaultEnabled modules (e.g., dora) activate without datasources.
		ext := reg.Get(name)
		if de, ok := ext.(module.DefaultEnabled); ok && de.DefaultEnabled() {
			if err := reg.InitModule(name, nil); err != nil {
				if errors.Is(err, module.ErrNoValidConfig) {
					a.log.WithField("module", name).Debug("Default-enabled module has no valid config, skipping")

					continue
				}

				return fmt.Errorf("initializing default-enabled module %q: %w", name, err)
			}

			continue
		}

		a.log.WithField("module", name).Debug("Module not configured, skipping")
	}

	a.log.WithField("initialized_count", len(reg.Initialized())).Info("Module registry built")

	return nil
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

func (a *App) newCartographoorClient() cartographoor.CartographoorClient {
	return cartographoor.NewCartographoorClient(a.log, cartographoor.CartographoorConfig{
		URL:      cartographoor.DefaultCartographoorURL,
		CacheTTL: cartographoor.DefaultCacheTTL,
		Timeout:  cartographoor.DefaultHTTPTimeout,
	})
}

func (a *App) injectProxyClient() {
	a.ModuleRegistry.InjectProxyAccess(a.ProxyClient)
}

func (a *App) injectCartographoorClient() {
	a.ModuleRegistry.InjectCartographoorClient(a.Cartographoor)
}
