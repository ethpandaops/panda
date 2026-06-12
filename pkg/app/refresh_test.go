package app

import (
	"context"
	"io"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/config"
	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/proxy"
	"github.com/ethpandaops/panda/pkg/types"
)

// TestRefreshModulesActivatesNewlyDiscoverable verifies that a module which
// was skipped at startup (no relevant datasources) gets activated when the
// proxy client later discovers one — deps injected, Start called.
func TestRefreshModulesActivatesNewlyDiscoverable(t *testing.T) {
	t.Parallel()

	log := logrus.New()
	log.SetOutput(io.Discard)

	reg := module.NewRegistry(log)
	mod := &fakeDiscoverableModule{name: "fake"}
	reg.Add(mod)

	client := &fakeProxyClient{
		clickhouse: []types.DatasourceInfo{{Type: "clickhouse", Name: "clickhouse-raw"}},
	}

	a := &App{
		log:            log,
		ModuleRegistry: reg,
		ProxyClient:    client,
	}

	if reg.IsInitialized("fake") {
		t.Fatal("module unexpectedly initialized before refresh")
	}

	a.refreshModulesFromDiscovery()

	if !reg.IsInitialized("fake") {
		t.Fatal("refreshModulesFromDiscovery did not activate the module")
	}

	if mod.startCalls.Load() != 1 {
		t.Fatalf("module.Start was called %d times, want 1", mod.startCalls.Load())
	}

	if !mod.proxyInjected.Load() {
		t.Fatal("proxy client was not injected into the newly-activated module")
	}

	if mod.reloadCalls.Load() != 0 {
		t.Fatalf("OnDiscoveryReloaded was called %d times for a newly-activated module, want 0",
			mod.reloadCalls.Load())
	}
}

func TestLocalProxyServerConfigMapsAutodiscover(t *testing.T) {
	t.Parallel()

	enabled := true
	app := New(logrus.New(), &config.Config{
		LocalProxy: config.LocalProxyConfig{
			Enabled: &enabled,
			ClickHouse: []config.LocalProxyClickHouseConfig{
				{
					Name:                 "local-kurtosis",
					Description:          "Custom local Kurtosis datasource",
					Host:                 "localhost",
					Port:                 18123,
					Database:             "otel",
					Username:             "default",
					Password:             "secret",
					Secure:               true,
					Autodiscover:         true,
					AutodiscoverInterval: 15 * time.Second,
				},
			},
		},
	})

	cfg := app.localProxyServerConfig()

	if got := cfg.Server.ListenAddr; got != "127.0.0.1:0" {
		t.Fatalf("ListenAddr = %q, want 127.0.0.1:0", got)
	}
	if got := cfg.Auth.Mode; got != proxy.AuthModeNone {
		t.Fatalf("Auth mode = %q, want none", got)
	}
	if len(cfg.ClickHouse) != 1 {
		t.Fatalf("ClickHouse length = %d, want 1", len(cfg.ClickHouse))
	}

	got := cfg.ClickHouse[0]
	if got.Name != "local-kurtosis" || got.Host != "localhost" || got.Port != 18123 ||
		got.Description != "Custom local Kurtosis datasource" || got.Database != "otel" ||
		got.Username != "default" || got.Password != "secret" || !got.Secure ||
		!got.Autodiscover || got.AutodiscoverInterval != 15*time.Second {
		t.Fatalf("ClickHouse config = %#v, want mapped local proxy config", got)
	}
}

func TestRefreshModulesFromDiscoverySerializesConcurrentCallbacks(t *testing.T) {
	t.Parallel()

	log := logrus.New()
	log.SetOutput(io.Discard)

	reg := module.NewRegistry(log)
	mod := &fakeDiscoverableModule{
		name:       "fake",
		startDelay: 10 * time.Millisecond,
	}
	reg.Add(mod)

	client := &fakeProxyClient{
		clickhouse: []types.DatasourceInfo{{Type: "clickhouse", Name: "xatu"}},
	}

	a := &App{
		log:            log,
		ModuleRegistry: reg,
		ProxyClient:    client,
	}

	const refreshes = 8
	var wg sync.WaitGroup
	for i := 0; i < refreshes; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.refreshModulesFromDiscovery()
		}()
	}
	wg.Wait()

	if mod.startCalls.Load() != 1 {
		t.Fatalf("module.Start was called %d times, want 1", mod.startCalls.Load())
	}
	if mod.maxActiveStarts.Load() > 1 {
		t.Fatalf("module.Start ran concurrently %d times, want serialized starts", mod.maxActiveStarts.Load())
	}
	if mod.reloadCalls.Load() != refreshes-1 {
		t.Fatalf("OnDiscoveryReloaded was called %d times, want %d",
			mod.reloadCalls.Load(), refreshes-1)
	}
}

// TestRefreshModulesClearsStateWhenAllDatasourcesDisappear verifies that an
// already-running module whose datasources have all disappeared gets its list
// cleared and OnDiscoveryReloaded still fires — so panda datasources, sandbox
// env, and schema discovery don't keep reporting clusters that no longer
// exist.
func TestRefreshModulesClearsStateWhenAllDatasourcesDisappear(t *testing.T) {
	t.Parallel()

	log := logrus.New()
	log.SetOutput(io.Discard)

	reg := module.NewRegistry(log)
	mod := &fakeDiscoverableModule{name: "fake"}
	reg.Add(mod)

	if err := reg.InitModuleFromDiscovery("fake",
		[]types.DatasourceInfo{{Type: "clickhouse", Name: "clickhouse-raw"}}); err != nil {
		t.Fatalf("initial InitModuleFromDiscovery error = %v", err)
	}

	if got := len(mod.lastInit); got != 1 {
		t.Fatalf("after initial init: module has %d datasources, want 1", got)
	}

	// Simulate every ClickHouse datasource being removed.
	client := &fakeProxyClient{clickhouse: nil}

	a := &App{
		log:            log,
		ModuleRegistry: reg,
		ProxyClient:    client,
	}

	a.refreshModulesFromDiscovery()

	if got := len(mod.lastInit); got != 0 {
		t.Fatalf("after all-disappear refresh: module retained %d datasources, want 0", got)
	}

	if mod.reloadCalls.Load() != 1 {
		t.Fatalf("OnDiscoveryReloaded was called %d times after all-disappear refresh, want 1",
			mod.reloadCalls.Load())
	}
}

// TestRefreshModulesReloadsAlreadyRunning verifies that a module already in
// the initialized set gets its DiscoveryReloadable hook called (and is not
// re-Started) when the proxy refreshes.
func TestRefreshModulesReloadsAlreadyRunning(t *testing.T) {
	t.Parallel()

	log := logrus.New()
	log.SetOutput(io.Discard)

	reg := module.NewRegistry(log)
	mod := &fakeDiscoverableModule{name: "fake"}
	reg.Add(mod)

	// Simulate startup: a module already initialized + started before refresh.
	if err := reg.InitModuleFromDiscovery("fake",
		[]types.DatasourceInfo{{Type: "clickhouse", Name: "clickhouse-raw"}}); err != nil {
		t.Fatalf("initial InitModuleFromDiscovery error = %v", err)
	}

	client := &fakeProxyClient{
		clickhouse: []types.DatasourceInfo{
			{Type: "clickhouse", Name: "clickhouse-raw"},
			{Type: "clickhouse", Name: "new-cluster"},
		},
	}

	a := &App{
		log:            log,
		ModuleRegistry: reg,
		ProxyClient:    client,
	}

	a.refreshModulesFromDiscovery()

	if mod.startCalls.Load() != 0 {
		t.Fatalf("module.Start was called on already-running module (%d times)", mod.startCalls.Load())
	}

	if mod.reloadCalls.Load() != 1 {
		t.Fatalf("OnDiscoveryReloaded was called %d times, want 1", mod.reloadCalls.Load())
	}

	if got := len(mod.lastInit); got != 2 {
		t.Fatalf("module received %d datasources on refresh, want 2", got)
	}
}

type fakeDiscoverableModule struct {
	name string

	startCalls    atomic.Int32
	reloadCalls   atomic.Int32
	proxyInjected atomic.Bool

	activeStarts    atomic.Int32
	maxActiveStarts atomic.Int32
	startDelay      time.Duration

	lastInit []types.DatasourceInfo
}

func (m *fakeDiscoverableModule) Name() string                 { return m.name }
func (m *fakeDiscoverableModule) Init(_ []byte) error          { return nil }
func (m *fakeDiscoverableModule) ApplyDefaults()               {}
func (m *fakeDiscoverableModule) Validate() error              { return nil }
func (m *fakeDiscoverableModule) Stop(_ context.Context) error { return nil }
func (m *fakeDiscoverableModule) Start(_ context.Context) error {
	active := m.activeStarts.Add(1)
	defer m.activeStarts.Add(-1)

	for {
		maxActive := m.maxActiveStarts.Load()
		if active <= maxActive || m.maxActiveStarts.CompareAndSwap(maxActive, active) {
			break
		}
	}

	m.startCalls.Add(1)
	if m.startDelay > 0 {
		time.Sleep(m.startDelay)
	}

	return nil
}

func (m *fakeDiscoverableModule) InitFromDiscovery(datasources []types.DatasourceInfo) error {
	filtered := make([]types.DatasourceInfo, 0, len(datasources))
	for _, ds := range datasources {
		if ds.Type == "clickhouse" {
			filtered = append(filtered, ds)
		}
	}

	m.lastInit = filtered

	if len(filtered) == 0 {
		return module.ErrNoValidConfig
	}

	return nil
}

func (m *fakeDiscoverableModule) SetProxyClient(_ proxy.Service) {
	m.proxyInjected.Store(true)
}

func (m *fakeDiscoverableModule) OnDiscoveryReloaded(_ context.Context) error {
	m.reloadCalls.Add(1)
	return nil
}

// fakeProxyClient is a stub that returns canned datasources. Only the
// datasource-getter methods are exercised by refreshModulesFromDiscovery.
type fakeProxyClient struct {
	clickhouse   []types.DatasourceInfo
	prometheus   []types.DatasourceInfo
	loki         []types.DatasourceInfo
	benchmarkoor []types.DatasourceInfo
	ethnode      bool
}

func (f *fakeProxyClient) Start(_ context.Context) error               { return nil }
func (f *fakeProxyClient) Stop(_ context.Context) error                { return nil }
func (f *fakeProxyClient) URL() string                                 { return "" }
func (f *fakeProxyClient) RegisterToken() string                       { return "" }
func (f *fakeProxyClient) RevokeToken()                                {}
func (f *fakeProxyClient) Discover(_ context.Context) error            { return nil }
func (f *fakeProxyClient) EnsureAuthenticated(_ context.Context) error { return nil }

func (f *fakeProxyClient) ClickHouseDatasources() []string {
	names := make([]string, 0, len(f.clickhouse))
	for _, ds := range f.clickhouse {
		names = append(names, ds.Name)
	}
	return names
}

func (f *fakeProxyClient) ClickHouseDatasourceInfo() []types.DatasourceInfo { return f.clickhouse }

func (f *fakeProxyClient) ClickHouseQuery(_ context.Context, _, _ string, _ url.Values) ([]byte, error) {
	return nil, nil
}

func (f *fakeProxyClient) PrometheusDatasources() []string {
	names := make([]string, 0, len(f.prometheus))
	for _, ds := range f.prometheus {
		names = append(names, ds.Name)
	}
	return names
}

func (f *fakeProxyClient) PrometheusDatasourceInfo() []types.DatasourceInfo { return f.prometheus }

func (f *fakeProxyClient) LokiDatasources() []string {
	names := make([]string, 0, len(f.loki))
	for _, ds := range f.loki {
		names = append(names, ds.Name)
	}
	return names
}

func (f *fakeProxyClient) LokiDatasourceInfo() []types.DatasourceInfo { return f.loki }

func (f *fakeProxyClient) BenchmarkoorDatasourceInfo() []types.DatasourceInfo {
	return f.benchmarkoor
}

func (f *fakeProxyClient) EthNodeAvailable() bool { return f.ethnode }

func (f *fakeProxyClient) EthNodeDatasourceInfo() []types.DatasourceInfo {
	if !f.ethnode {
		return nil
	}

	return []types.DatasourceInfo{{Type: "ethnode", Name: "ethnode"}}
}

func (f *fakeProxyClient) EmbeddingAvailable() bool { return false }
func (f *fakeProxyClient) EmbeddingModel() string   { return "" }

// TestDiscoveryRefreshDisarmedUntilBuildCompletes verifies the discovery hook
// is inert before ArmDiscoveryRefresh: background ticks during server build
// must not activate modules concurrently with registry wiring.
func TestDiscoveryRefreshDisarmedUntilBuildCompletes(t *testing.T) {
	t.Parallel()

	log := logrus.New()
	log.SetOutput(io.Discard)

	reg := module.NewRegistry(log)
	mod := &fakeDiscoverableModule{name: "fake"}
	reg.Add(mod)

	client := &fakeProxyClient{
		clickhouse: []types.DatasourceInfo{{Type: "clickhouse", Name: "clickhouse-raw"}},
	}

	a := &App{
		log:            log,
		ModuleRegistry: reg,
		ProxyClient:    client,
	}

	a.onDiscoveryRefresh()

	if reg.IsInitialized("fake") {
		t.Fatal("disarmed discovery hook activated a module")
	}

	registered := atomic.Int32{}

	a.ArmDiscoveryRefresh(func(_ module.Module) {
		registered.Add(1)
	})

	if !reg.IsInitialized("fake") {
		t.Fatal("ArmDiscoveryRefresh did not run the catch-up refresh")
	}

	if registered.Load() != 1 {
		t.Fatalf("registrar called %d times for the late-activated module, want 1", registered.Load())
	}

	a.onDiscoveryRefresh()

	if registered.Load() != 1 {
		t.Fatalf("registrar re-ran for an already-active module: %d calls", registered.Load())
	}
}
