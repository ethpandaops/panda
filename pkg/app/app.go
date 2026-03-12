// Package app provides the shared application core used by the server runtime.
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
	ProxyService   proxy.Service
	Cartographoor  cartographoor.CartographoorClient

	moduleRegistryBuilder func() (*module.Registry, error)
	sandboxBuilder        func() (sandbox.Service, error)
	proxyServiceBuilder   func() proxy.Service
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
	app.proxyServiceBuilder = app.buildProxyService
	app.cartographoorBuilder = app.newCartographoorClient

	return app
}

// Config returns the application configuration.
func (a *App) Config() *config.Config {
	return a.cfg
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

	if a.ProxyService != nil {
		_ = a.ProxyService.Stop(ctx)
	}

	if a.Sandbox != nil {
		_ = a.Sandbox.Stop(ctx)
	}
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
func (a *App) initModules(proxySvc proxy.Service) error {
	reg := a.ModuleRegistry

	snapshot := proxySvc.Datasources()

	// Collect discovered datasources.
	var discovered []types.DatasourceInfo
	discovered = append(discovered, proxy.FilterDatasourceInfoByType(snapshot.Datasources, "clickhouse")...)
	discovered = append(discovered, proxy.FilterDatasourceInfoByType(snapshot.Datasources, "prometheus")...)
	discovered = append(discovered, proxy.FilterDatasourceInfoByType(snapshot.Datasources, "loki")...)

	if snapshot.EthNodeAvailable {
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

func (a *App) buildProxyService() proxy.Service {
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
