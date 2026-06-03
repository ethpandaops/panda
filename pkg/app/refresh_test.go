package app

import (
	"context"
	"io"
	"sync/atomic"
	"testing"

	"github.com/sirupsen/logrus"

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

	lastInit []types.DatasourceInfo
}

func (m *fakeDiscoverableModule) Name() string                  { return m.name }
func (m *fakeDiscoverableModule) Init(_ []byte) error           { return nil }
func (m *fakeDiscoverableModule) ApplyDefaults()                {}
func (m *fakeDiscoverableModule) Validate() error               { return nil }
func (m *fakeDiscoverableModule) Stop(_ context.Context) error  { return nil }
func (m *fakeDiscoverableModule) Start(_ context.Context) error { m.startCalls.Add(1); return nil }

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
	clickhouse []types.DatasourceInfo
	prometheus []types.DatasourceInfo
	loki       []types.DatasourceInfo
	ethnode    bool
}

func (f *fakeProxyClient) Start(_ context.Context) error               { return nil }
func (f *fakeProxyClient) Stop(_ context.Context) error                { return nil }
func (f *fakeProxyClient) URL() string                                 { return "" }
func (f *fakeProxyClient) RegisterToken(_ string) string               { return "" }
func (f *fakeProxyClient) RevokeToken(_ string)                        {}
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

func (f *fakeProxyClient) EthNodeAvailable() bool   { return f.ethnode }
func (f *fakeProxyClient) EmbeddingAvailable() bool { return false }
func (f *fakeProxyClient) EmbeddingModel() string   { return "" }
