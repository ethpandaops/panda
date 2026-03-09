// Package app provides the shared application core used by both the MCP server and the CLI.
// It handles extension initialization, proxy connection, sandbox setup, and semantic search indices.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/mcp/pkg/cartographoor"
	"github.com/ethpandaops/mcp/pkg/config"
	"github.com/ethpandaops/mcp/pkg/extension"
	"github.com/ethpandaops/mcp/pkg/proxy"
	"github.com/ethpandaops/mcp/pkg/sandbox"

	clickhouseextension "github.com/ethpandaops/mcp/extensions/clickhouse"
	doraextension "github.com/ethpandaops/mcp/extensions/dora"
	ethnodeextension "github.com/ethpandaops/mcp/extensions/ethnode"
	lokiextension "github.com/ethpandaops/mcp/extensions/loki"
	prometheusextension "github.com/ethpandaops/mcp/extensions/prometheus"
)

// App contains the shared core components used by both the MCP server and CLI.
type App struct {
	log logrus.FieldLogger
	cfg *config.Config

	ExtensionRegistry *extension.Registry
	Sandbox           sandbox.Service
	ProxyClient       proxy.Client
	Cartographoor     cartographoor.CartographoorClient
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
// extension registry -> sandbox -> proxy -> extensions start -> cartographoor -> search indices.
func (a *App) Build(ctx context.Context) error {
	a.log.Info("Building application dependencies")

	// 1. Build and initialize extension registry.
	extensionReg, err := a.buildExtensionRegistry()
	if err != nil {
		return fmt.Errorf("building extension registry: %w", err)
	}

	a.ExtensionRegistry = extensionReg

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

	// 4. Inject proxy client into extensions and start all extensions.
	a.injectProxyClient()

	if err := a.ExtensionRegistry.StartAll(ctx); err != nil {
		a.stop(ctx)

		return fmt.Errorf("starting extensions: %w", err)
	}

	a.log.Info("All extensions started")

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

	// 6. Inject cartographoor client into extensions.
	a.injectCartographoorClient()

	return nil
}

// BuildLight initializes only the extension registry and proxy client.
// Extensions are started (e.g., schema discovery) but sandbox, cartographoor,
// and semantic search indices are not created.
func (a *App) BuildLight(ctx context.Context) error {
	a.log.Info("Building lightweight application dependencies")

	extensionReg, err := a.buildExtensionRegistry()
	if err != nil {
		return fmt.Errorf("building extension registry: %w", err)
	}

	a.ExtensionRegistry = extensionReg

	proxyClient := a.buildProxyClient()
	if err := proxyClient.Start(ctx); err != nil {
		return fmt.Errorf("starting proxy client: %w", err)
	}

	a.ProxyClient = proxyClient
	a.log.WithField("url", proxyClient.URL()).Info("Proxy client connected")

	a.injectProxyClient()

	if err := a.ExtensionRegistry.StartAll(ctx); err != nil {
		a.stop(ctx)

		return fmt.Errorf("starting extensions: %w", err)
	}

	a.log.Info("All extensions started")

	return nil
}

// BuildWithSandbox initializes extensions, proxy, and sandbox — but skips
// cartographoor and semantic search indices. Used by the CLI execute command.
func (a *App) BuildWithSandbox(ctx context.Context) error {
	a.log.Info("Building application dependencies (with sandbox)")

	extensionReg, err := a.buildExtensionRegistry()
	if err != nil {
		return fmt.Errorf("building extension registry: %w", err)
	}

	a.ExtensionRegistry = extensionReg

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

	if err := a.ExtensionRegistry.StartAll(ctx); err != nil {
		a.stop(ctx)

		return fmt.Errorf("starting extensions: %w", err)
	}

	a.log.Info("All extensions started")

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

	if a.ExtensionRegistry != nil {
		a.ExtensionRegistry.StopAll(ctx)
	}

	if a.ProxyClient != nil {
		_ = a.ProxyClient.Stop(ctx)
	}

	if a.Sandbox != nil {
		_ = a.Sandbox.Stop(ctx)
	}
}

func (a *App) buildExtensionRegistry() (*extension.Registry, error) {
	reg := extension.NewRegistry(a.log)

	// Register all compiled-in extensions.
	reg.Add(clickhouseextension.New())
	reg.Add(doraextension.New())
	reg.Add(ethnodeextension.New())
	reg.Add(lokiextension.New())
	reg.Add(prometheusextension.New())

	// Initialize extensions that have config or are default-enabled.
	for _, name := range reg.All() {
		rawYAML, err := a.cfg.ExtensionConfigYAML(name)
		if err != nil {
			return nil, fmt.Errorf("getting config for extension %q: %w", name, err)
		}

		if rawYAML == nil {
			// Check if the extension is default-enabled.
			ext := reg.Get(name)
			if de, ok := ext.(extension.DefaultEnabled); ok && de.DefaultEnabled() {
				if err := reg.InitExtension(name, nil); err != nil {
					if errors.Is(err, extension.ErrNoValidConfig) {
						a.log.WithField("extension", name).Debug("Default-enabled extension has no valid config, skipping")

						continue
					}

					return nil, fmt.Errorf("initializing default-enabled extension %q: %w", name, err)
				}

				continue
			}

			a.log.WithField("extension", name).Debug("Extension not configured, skipping")

			continue
		}

		if err := reg.InitExtension(name, rawYAML); err != nil {
			// Skip if no valid config (e.g., env vars not set).
			if errors.Is(err, extension.ErrNoValidConfig) {
				a.log.WithField("extension", name).Debug("Extension has no valid config entries, skipping")

				continue
			}

			return nil, fmt.Errorf("initializing extension %q: %w", name, err)
		}
	}

	a.log.WithField("initialized_count", len(reg.Initialized())).Info("Extension registry built")

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
	for _, ext := range a.ExtensionRegistry.Initialized() {
		if aware, ok := ext.(extension.ProxyAware); ok {
			aware.SetProxyClient(a.ProxyClient)
			a.log.WithField("extension", ext.Name()).Debug("Injected proxy client into extension")
		}
	}
}

func (a *App) injectCartographoorClient() {
	for _, ext := range a.ExtensionRegistry.Initialized() {
		if aware, ok := ext.(extension.CartographoorAware); ok {
			aware.SetCartographoorClient(a.Cartographoor)
			a.log.WithField("extension", ext.Name()).Debug("Injected cartographoor client into extension")
		}
	}
}

// SandboxEnv builds credential-free environment variables for sandbox execution.
// Includes proxy URL, datasource info, and S3 bucket — but no credentials.
func (a *App) SandboxEnv() (map[string]string, error) {
	env, err := a.ExtensionRegistry.SandboxEnv()
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

	// Datasources are proxy-authoritative; override extension-provided lists.
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
