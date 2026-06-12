// Package app provides the shared application core used by both the MCP server and the CLI.
// It handles module initialization, proxy connection, sandbox setup, and semantic search indices.
package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/cartographoor"
	"github.com/ethpandaops/panda/pkg/config"
	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/proxy"
	"github.com/ethpandaops/panda/pkg/sandbox"
	"github.com/ethpandaops/panda/pkg/types"

	datasetsmodule "github.com/ethpandaops/panda/datasets"
	benchmarkoormodule "github.com/ethpandaops/panda/modules/benchmarkoor"
	blockarchivemodule "github.com/ethpandaops/panda/modules/block_archive"
	cbtmodule "github.com/ethpandaops/panda/modules/cbt"
	clickhousemodule "github.com/ethpandaops/panda/modules/clickhouse"
	doramodule "github.com/ethpandaops/panda/modules/dora"
	ethnodemodule "github.com/ethpandaops/panda/modules/ethnode"
	forkymodule "github.com/ethpandaops/panda/modules/forky"
	lokimodule "github.com/ethpandaops/panda/modules/loki"
	prometheusmodule "github.com/ethpandaops/panda/modules/prometheus"
	tracoormodule "github.com/ethpandaops/panda/modules/tracoor"
)

// refreshActivationTimeout caps Start + OnDiscoveryReloaded calls dispatched
// from the proxy client's discovery hook so a slow module can't stall the
// discovery goroutine.
const refreshActivationTimeout = 30 * time.Second

const embeddedLocalProxyName = "local"

// localProxyDiscoveryInterval is how often the server re-discovers datasources
// from the in-process local proxy. It is short (vs the 60s default for external
// proxies) because the local proxy is loopback and cheap to poll, so
// autodiscovered local datasources surface within a few seconds.
const localProxyDiscoveryInterval = 5 * time.Second

// App contains the shared core components used by both the MCP server and CLI.
type App struct {
	log logrus.FieldLogger
	cfg *config.Config

	ModuleRegistry *module.Registry
	Sandbox        sandbox.Service
	ProxyClient    proxy.Client
	LocalProxy     proxy.Server
	Cartographoor  cartographoor.CartographoorClient

	refreshMu sync.Mutex

	// discoveryRefreshArmed gates the proxy discovery hook: until the server
	// build completes, module activation would race registry wiring on the
	// main goroutine.
	discoveryRefreshArmed atomic.Bool

	registrarMu sync.Mutex
	// moduleResourceRegistrar registers a late-activated module's resources
	// with the (already-built) resource registry. Installed by the server
	// builder when it arms the discovery refresh.
	moduleResourceRegistrar func(module.Module)
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
	proxyClient, err := a.buildProxyClient(ctx, a.onDiscoveryRefresh)
	if err != nil {
		_ = a.stop(ctx)

		return fmt.Errorf("building proxy client: %w", err)
	}

	if err := proxyClient.Start(ctx); err != nil {
		_ = a.stop(ctx)

		return fmt.Errorf("starting proxy client: %w", err)
	}

	a.ProxyClient = proxyClient
	a.log.WithField("url", proxyClient.URL()).Info("Proxy client connected")

	// 4. Initialize modules.
	if err := a.initModules(proxyClient); err != nil {
		_ = a.stop(ctx)

		return fmt.Errorf("initializing modules: %w", err)
	}

	// 5. Inject proxy client into modules and start all modules.
	a.injectProxyClient()

	if err := a.ModuleRegistry.StartAll(ctx); err != nil {
		_ = a.stop(ctx)

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
		_ = a.stop(ctx)

		return fmt.Errorf("starting cartographoor client: %w", err)
	}

	a.Cartographoor = cartographoorClient
	a.log.Info("Cartographoor client started")

	// 7. Inject cartographoor client into modules.
	a.injectCartographoorClient()

	return nil
}

// Stop cleans up all started components in reverse order, joining any
// component shutdown errors.
func (a *App) Stop(ctx context.Context) error {
	return a.stop(ctx)
}

func (a *App) stop(ctx context.Context) error {
	var errs []error

	if a.Cartographoor != nil {
		if err := a.Cartographoor.Stop(); err != nil {
			errs = append(errs, fmt.Errorf("stopping cartographoor client: %w", err))
		}
	}

	if a.ModuleRegistry != nil {
		a.ModuleRegistry.StopAll(ctx)
	}

	if a.ProxyClient != nil {
		if err := a.ProxyClient.Stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("stopping proxy client: %w", err))
		}
	}

	if a.LocalProxy != nil {
		_ = a.LocalProxy.Stop(ctx)
	}

	if a.Sandbox != nil {
		if err := a.Sandbox.Stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("stopping sandbox: %w", err))
		}
	}

	return errors.Join(errs...)
}

// registerModules creates a module registry and registers all compiled-in
// modules without initializing them.
func (a *App) registerModules() *module.Registry {
	reg := module.NewRegistry(a.log)

	reg.Add(benchmarkoormodule.New())
	reg.Add(blockarchivemodule.New())
	reg.Add(cbtmodule.New())
	reg.Add(clickhousemodule.New())
	reg.Add(datasetsmodule.New())
	reg.Add(doramodule.New())
	reg.Add(ethnodemodule.New())
	reg.Add(forkymodule.New())
	reg.Add(lokimodule.New())
	reg.Add(prometheusmodule.New())
	reg.Add(tracoormodule.New())

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

func (a *App) buildProxyClient(ctx context.Context, onDiscover func()) (proxy.Client, error) {
	proxyConfigs := a.cfg.Proxies
	if len(proxyConfigs) == 0 && strings.TrimSpace(a.cfg.Proxy.URL) != "" {
		proxyConfigs = []config.ProxyConfig{a.cfg.Proxy}
	}

	routes := make([]proxy.ClientRoute, 0, len(proxyConfigs)+1)
	for _, proxyCfg := range proxyConfigs {
		clientCfg := a.proxyClientConfig(proxyCfg, onDiscover)
		routes = append(routes, proxy.ClientRoute{
			Name:   proxyCfg.Name,
			Client: proxy.NewClient(a.log, clientCfg),
		})
	}

	localRoute, err := a.startLocalProxyRoute(ctx, onDiscover)
	if err != nil {
		return nil, err
	}
	if localRoute != nil {
		routes = append(routes, *localRoute)
	}

	return proxy.NewRouter(a.log, routes), nil
}

func (a *App) proxyClientConfig(proxyCfg config.ProxyConfig, onDiscover func()) proxy.ClientConfig {
	cfg := proxy.ClientConfig{
		Name:       proxyCfg.Name,
		URL:        proxyCfg.URL,
		OnDiscover: onDiscover,
	}

	if proxyCfg.Auth == nil {
		return cfg
	}

	cfg.IssuerURL = proxyCfg.Auth.IssuerURL
	cfg.ClientID = proxyCfg.Auth.ClientID
	cfg.Resource = strings.TrimSpace(proxyCfg.Auth.Resource)
	cfg.RefreshTokenTTL = proxyCfg.Auth.RefreshTokenTTL
	cfg.AuthMode = strings.TrimSpace(proxyCfg.Auth.Mode)
	cfg.Username = strings.TrimSpace(proxyCfg.Auth.Username)
	cfg.Password = proxyCfg.Auth.Password

	// The legacy "oauth" embedded-issuer mode defaults the RFC 8707 resource
	// to the proxy URL; external-issuer modes (oidc, client_credentials) do not.
	if cfg.Resource == "" && cfg.AuthMode != "oidc" && cfg.AuthMode != proxy.AuthModeClientCredentials {
		cfg.Resource = proxyCfg.URL
	}

	return cfg
}

func (a *App) startLocalProxyRoute(ctx context.Context, onDiscover func()) (*proxy.ClientRoute, error) {
	if !a.cfg.LocalProxy.IsEnabled() {
		a.log.Debug("Embedded local proxy disabled")

		return nil, nil
	}

	cfg := a.localProxyServerConfig()
	localProxy, err := proxy.NewServer(a.log, cfg)
	if err != nil {
		return nil, fmt.Errorf("creating embedded local proxy: %w", err)
	}

	if err := localProxy.Start(ctx); err != nil {
		return nil, fmt.Errorf("starting embedded local proxy: %w", err)
	}

	a.LocalProxy = localProxy
	a.log.WithField("url", localProxy.URL()).Info("Embedded local proxy started")

	return &proxy.ClientRoute{
		Name:  embeddedLocalProxyName,
		Local: true,
		Client: proxy.NewClient(a.log, proxy.ClientConfig{
			Name:              embeddedLocalProxyName,
			URL:               localProxy.URL(),
			OnDiscover:        onDiscover,
			DiscoveryInterval: localProxyDiscoveryInterval,
		}),
	}, nil
}

func (a *App) localProxyServerConfig() proxy.ServerConfig {
	clickhouse := make([]proxy.ClickHouseClusterConfig, 0, len(a.cfg.LocalProxy.ClickHouse))
	for _, item := range a.cfg.LocalProxy.ClickHouse {
		contains := make([]proxy.DatasetBindingConfig, 0, len(item.Contains))
		for _, b := range item.Contains {
			contains = append(contains, proxy.DatasetBindingConfig{
				Dataset: b.Dataset,
				Params:  b.Params,
				Notes:   b.Notes,
			})
		}

		clickhouse = append(clickhouse, proxy.ClickHouseClusterConfig{
			BaseDatasourceConfig: proxy.BaseDatasourceConfig{
				Name:        item.Name,
				Description: item.Description,
			},
			Host:                 item.Host,
			Port:                 item.Port,
			Database:             item.Database,
			Username:             item.Username,
			Password:             item.Password,
			Secure:               item.Secure,
			Autodiscover:         item.Autodiscover,
			AutodiscoverInterval: item.AutodiscoverInterval,
			Contains:             contains,
		})
	}

	return proxy.ServerConfig{
		Server: proxy.HTTPServerConfig{
			ListenAddr: "127.0.0.1:0",
		},
		Auth: proxy.AuthConfig{
			Mode: proxy.AuthModeNone,
		},
		ClickHouse: clickhouse,
	}
}

// onDiscoveryRefresh is the proxy clients' discovery hook. It is inert until
// the server build completes: the background refresh (the embedded local proxy
// ticks every 5s) would otherwise activate modules concurrently with registry
// wiring on the main goroutine.
func (a *App) onDiscoveryRefresh() {
	if !a.discoveryRefreshArmed.Load() {
		return
	}

	a.refreshModulesFromDiscovery()
}

// ArmDiscoveryRefresh enables the background discovery hook and installs the
// registrar used to register resources for modules that activate after the
// server is built (e.g. a local Kurtosis datasource appearing later). It runs
// one refresh immediately to catch datasources discovered while the hook was
// still disarmed.
func (a *App) ArmDiscoveryRefresh(registrar func(module.Module)) {
	a.registrarMu.Lock()
	a.moduleResourceRegistrar = registrar
	a.registrarMu.Unlock()

	a.discoveryRefreshArmed.Store(true)

	a.refreshModulesFromDiscovery()
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
	a.refreshMu.Lock()
	defer a.refreshMu.Unlock()

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

	a.registrarMu.Lock()
	registrar := a.moduleResourceRegistrar
	a.registrarMu.Unlock()

	if registrar != nil {
		registrar(ext)
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
	discovered = append(discovered, proxyClient.EthNodeDatasourceInfo()...)
	discovered = append(discovered, proxyClient.BenchmarkoorDatasourceInfo()...)

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
