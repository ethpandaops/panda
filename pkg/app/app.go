// Package app provides the shared application core used by both the MCP server and the CLI.
// It handles module initialization, proxy connection, sandbox setup, and semantic search indices.
package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/cartographoor"
	"github.com/ethpandaops/panda/pkg/config"
	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/proxy"
	"github.com/ethpandaops/panda/pkg/sandbox"
	"github.com/ethpandaops/panda/pkg/types"

	blockarchivemodule "github.com/ethpandaops/panda/modules/block_archive"
	cbtmodule "github.com/ethpandaops/panda/modules/cbt"
	clickhousemodule "github.com/ethpandaops/panda/modules/clickhouse"
	doramodule "github.com/ethpandaops/panda/modules/dora"
	ethnodemodule "github.com/ethpandaops/panda/modules/ethnode"
	lokimodule "github.com/ethpandaops/panda/modules/loki"
	prometheusmodule "github.com/ethpandaops/panda/modules/prometheus"
)

// refreshActivationTimeout caps Start + OnDiscoveryReloaded calls dispatched
// from the proxy client's discovery hook so a slow module can't stall the
// discovery goroutine.
const refreshActivationTimeout = 30 * time.Second

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
// register modules -> sandbox -> proxy -> init modules -> module startup -> cartographoor.
func (a *App) Build(ctx context.Context) error {
	a.log.Info("Building application dependencies")

	// 1. Register all compiled-in modules (no initialization yet).
	moduleReg := a.registerModules()
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

	// 3. Create and start proxy client (performs initial discovery).
	// The OnDiscover hook fires on every successful refresh; it reapplies the
	// freshly discovered datasource list to ProxyDiscoverable modules so new
	// datasources show up without a server restart. During the initial
	// Discover (before step 4) no modules are initialized yet, so the hook is
	// a no-op until the first background tick.
	proxyClient := a.buildProxyClient(a.refreshModulesFromDiscovery)
	if err := proxyClient.Start(ctx); err != nil {
		a.stop(ctx)

		return fmt.Errorf("starting proxy client: %w", err)
	}

	a.ProxyClient = proxyClient
	a.log.WithField("url", proxyClient.URL()).Info("Proxy client connected")

	// 4. Initialize modules.
	if err := a.initModules(proxyClient); err != nil {
		a.stop(ctx)

		return fmt.Errorf("initializing modules: %w", err)
	}

	// 5. Inject proxy client into modules and start all modules.
	a.injectProxyClient()

	if err := a.ModuleRegistry.StartAll(ctx); err != nil {
		a.stop(ctx)

		return fmt.Errorf("starting modules: %w", err)
	}

	a.log.Info("All modules started")

	// 6. Create and start cartographoor client.
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

	// 7. Inject cartographoor client into modules.
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

// registerModules creates a module registry and registers all compiled-in
// modules without initializing them.
func (a *App) registerModules() *module.Registry {
	reg := module.NewRegistry(a.log)

	reg.Add(blockarchivemodule.New())
	reg.Add(cbtmodule.New())
	reg.Add(clickhousemodule.New())
	reg.Add(doramodule.New())
	reg.Add(ethnodemodule.New())
	reg.Add(lokimodule.New())
	reg.Add(prometheusmodule.New())

	return reg
}

// initModules initializes all registered modules.
func (a *App) initModules(proxyClient proxy.Client) error {
	reg := a.ModuleRegistry

	discovered := a.discoveredDatasources(proxyClient)

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

func (a *App) buildProxyClient(onDiscover func()) proxy.Client {
	cfg := proxy.ClientConfig{
		URL:        a.cfg.Proxy.URL,
		OnDiscover: onDiscover,
	}

	if a.cfg.Proxy.Auth != nil {
		cfg.IssuerURL = a.cfg.Proxy.Auth.IssuerURL
		cfg.ClientID = a.cfg.Proxy.Auth.ClientID
		cfg.Resource = strings.TrimSpace(a.cfg.Proxy.Auth.Resource)
		cfg.RefreshTokenTTL = a.cfg.Proxy.Auth.RefreshTokenTTL

		if cfg.Resource == "" && strings.TrimSpace(a.cfg.Proxy.Auth.Mode) != "oidc" {
			cfg.Resource = a.cfg.Proxy.URL
		}
	}

	return proxy.NewClient(a.log, cfg)
}

// refreshModulesFromDiscovery re-applies the proxy client's current datasource
// list to every ProxyDiscoverable module. Called from the proxy client's
// discovery hook so periodic refresh propagates to module state without
// restarting the server.
//
// Three behaviors:
//   - Already-running modules get their datasource list refreshed in place.
//     If they implement DiscoveryReloadable (e.g. clickhouse), state derived
//     from the list (schema discovery clients) is rebuilt as well.
//   - Modules that were skipped at startup because no relevant datasources
//     existed are activated: deps are injected and Start runs.
//   - Modules whose datasources have disappeared keep their last-seen state;
//     deactivating a running module isn't supported here.
func (a *App) refreshModulesFromDiscovery() {
	if a.ModuleRegistry == nil || a.ProxyClient == nil {
		return
	}

	// An empty list isn't a no-op signal — it means every previously-known
	// datasource is gone, and already-running modules need to clear their
	// state instead of holding on to stale entries.
	discovered := a.discoveredDatasources(a.ProxyClient)

	previouslyInitialized := initializedSet(a.ModuleRegistry)

	for _, name := range a.ModuleRegistry.All() {
		ext := a.ModuleRegistry.Get(name)
		if ext == nil {
			continue
		}

		if _, ok := ext.(module.ProxyDiscoverable); !ok {
			continue
		}

		if err := a.ModuleRegistry.InitModuleFromDiscovery(name, discovered); err != nil {
			// ErrNoValidConfig means the module has no datasources of its
			// type after this refresh. The module is required to write its
			// (empty) state before returning, so an already-running module
			// still gets to clean up downstream state in OnDiscoveryReloaded.
			// A not-yet-initialized module is skipped — there's nothing to
			// activate.
			if errors.Is(err, module.ErrNoValidConfig) {
				if !previouslyInitialized[name] {
					continue
				}
			} else {
				a.log.WithError(err).
					WithField("module", name).
					Warn("Failed to refresh module from proxy discovery")

				continue
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), refreshActivationTimeout)
	defer cancel()

	for _, ext := range a.ModuleRegistry.Initialized() {
		if previouslyInitialized[ext.Name()] {
			if reloadable, ok := ext.(module.DiscoveryReloadable); ok {
				if err := reloadable.OnDiscoveryReloaded(ctx); err != nil {
					a.log.WithError(err).
						WithField("module", ext.Name()).
						Warn("Module failed to reload after proxy discovery refresh")
				}
			}

			continue
		}

		a.activateModule(ctx, ext)
	}
}

// activateModule injects dependencies and starts a module that newly entered
// the initialized set during a refresh. Errors are logged, not returned, so a
// single misbehaving module can't stall the discovery loop.
func (a *App) activateModule(ctx context.Context, ext module.Module) {
	a.injectProxyClientInto(ext)
	a.injectCartographoorClientInto(ext)

	if err := ext.Start(ctx); err != nil {
		a.log.WithError(err).
			WithField("module", ext.Name()).
			Warn("Failed to start newly-initialized module after refresh")

		return
	}

	a.log.WithField("module", ext.Name()).Info("Module activated after proxy discovery refresh")
}

func initializedSet(reg *module.Registry) map[string]bool {
	initialized := reg.Initialized()
	set := make(map[string]bool, len(initialized))

	for _, ext := range initialized {
		set[ext.Name()] = true
	}

	return set
}

// discoveredDatasources collects the proxy client's current view of
// datasources across all types in the same order as initModules so refresh
// behavior matches startup.
func (a *App) discoveredDatasources(proxyClient proxy.Client) []types.DatasourceInfo {
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

	return discovered
}

func (a *App) injectProxyClient() {
	for _, ext := range a.ModuleRegistry.Initialized() {
		a.injectProxyClientInto(ext)
	}
}

func (a *App) injectCartographoorClient() {
	for _, ext := range a.ModuleRegistry.Initialized() {
		a.injectCartographoorClientInto(ext)
	}
}

func (a *App) injectProxyClientInto(ext module.Module) {
	if a.ProxyClient == nil {
		return
	}

	if aware, ok := ext.(module.ProxyAware); ok {
		aware.SetProxyClient(a.ProxyClient)
		a.log.WithField("module", ext.Name()).Debug("Injected proxy client into module")
	}
}

func (a *App) injectCartographoorClientInto(ext module.Module) {
	if a.Cartographoor == nil {
		return
	}

	if aware, ok := ext.(module.CartographoorAware); ok {
		aware.SetCartographoorClient(a.Cartographoor)
		a.log.WithField("module", ext.Name()).Debug("Injected cartographoor client into module")
	}
}
