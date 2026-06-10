package clickhouse

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/types"
)

// TestSchemaClient_UpdateDatasources_TriggersRefresh verifies that pushing a
// new datasource list into a running schema client signals an immediate
// refresh — this is what lets newly added ClickHouse clusters get their
// schemas discovered without a server restart.
func TestSchemaClient_UpdateDatasources_TriggersRefresh(t *testing.T) {
	t.Parallel()

	log := logrus.New()
	log.SetOutput(io.Discard)

	client := &clickhouseSchemaClient{
		log:         log.WithField("test", "true"),
		clusters:    make(map[string]*ClusterTables, 1),
		datasources: map[string]string{"clickhouse-raw": "clickhouse-raw"},
		refreshNow:  make(chan struct{}, 1),
	}

	client.UpdateDatasources([]SchemaDiscoveryDatasource{
		{Name: "clickhouse-raw", Cluster: "clickhouse-raw"},
		{Name: "clickhouse-refined", Cluster: "clickhouse-refined"},
	})

	snapshot := client.snapshotDatasources()
	if len(snapshot) != 2 || snapshot["clickhouse-refined"] != "clickhouse-refined" {
		t.Fatalf("snapshotDatasources() = %v, want clickhouse-raw + clickhouse-refined", snapshot)
	}

	select {
	case <-client.refreshNow:
		// Good — change signaled an immediate refresh.
	default:
		t.Fatal("UpdateDatasources did not signal refresh after datasource list changed")
	}
}

// TestSchemaClient_UpdateDatasources_NoChangeSkipsRefresh verifies the
// schema client doesn't signal refresh when the datasource list is unchanged.
// The proxy client refreshes every 5 minutes; we don't want to thrash schema
// discovery (each refresh hits every cluster) when nothing actually changed.
func TestSchemaClient_UpdateDatasources_NoChangeSkipsRefresh(t *testing.T) {
	t.Parallel()

	log := logrus.New()
	log.SetOutput(io.Discard)

	client := &clickhouseSchemaClient{
		log:         log.WithField("test", "true"),
		clusters:    make(map[string]*ClusterTables, 1),
		datasources: map[string]string{"clickhouse-raw": "clickhouse-raw"},
		refreshNow:  make(chan struct{}, 1),
	}

	client.UpdateDatasources([]SchemaDiscoveryDatasource{
		{Name: "clickhouse-raw", Cluster: "clickhouse-raw"},
	})

	select {
	case <-client.refreshNow:
		t.Fatal("UpdateDatasources signaled refresh despite no change to datasource list")
	default:
		// Good — no-op call did not trigger refresh.
	}
}

// TestModule_OnDiscoveryReloaded_PushesDatasourcesToSchemaClient verifies the
// clickhouse module forwards refreshed datasources to its schema client so
// schema discovery picks up new clusters without restart.
func TestModule_OnDiscoveryReloaded_PushesDatasourcesToSchemaClient(t *testing.T) {
	t.Parallel()

	fake := &fakeSchemaClient{}

	mod := New()
	mod.schemaClient = fake

	// Simulate the proxy discovery hook updating p.datasources.
	mod.dsMu.Lock()
	mod.datasources = []types.DatasourceInfo{
		{Type: "clickhouse", Name: "clickhouse-raw"},
		{Type: "clickhouse", Name: "clickhouse-refined"},
	}
	mod.dsMu.Unlock()

	if err := mod.OnDiscoveryReloaded(context.Background()); err != nil {
		t.Fatalf("OnDiscoveryReloaded error = %v", err)
	}

	if got := len(fake.lastUpdate); got != 2 {
		t.Fatalf("schema client received %d datasources, want 2", got)
	}

	if fake.lastUpdate[0].Name != "clickhouse-raw" || fake.lastUpdate[1].Name != "clickhouse-refined" {
		t.Fatalf("schema client received %v, want clickhouse-raw + clickhouse-refined", fake.lastUpdate)
	}
}

// TestModule_OnDiscoveryReloaded_RespectsYAMLConfig verifies YAML config wins
// over proxy discovery for schema discovery: when the user has pinned a
// specific list of clusters in config, refresh must not widen the set.
func TestModule_OnDiscoveryReloaded_RespectsYAMLConfig(t *testing.T) {
	t.Parallel()

	fake := &fakeSchemaClient{}

	mod := New()
	mod.schemaClient = fake
	mod.cfg.SchemaDiscovery.Datasources = []SchemaDiscoveryDatasource{
		{Name: "clickhouse-raw", Cluster: "clickhouse-raw"},
	}

	mod.dsMu.Lock()
	mod.datasources = []types.DatasourceInfo{
		{Type: "clickhouse", Name: "clickhouse-raw"},
		{Type: "clickhouse", Name: "new-cluster"},
	}
	mod.dsMu.Unlock()

	if err := mod.OnDiscoveryReloaded(context.Background()); err != nil {
		t.Fatalf("OnDiscoveryReloaded error = %v", err)
	}

	if fake.lastUpdate != nil {
		t.Fatalf("schema client got %v, want no update when YAML config is set", fake.lastUpdate)
	}
}

// TestModule_OnDiscoveryReloaded_NoSchemaClient is a smoke test: when schema
// discovery hasn't started (or is disabled) the reload hook must not panic.
func TestModule_OnDiscoveryReloaded_NoSchemaClient(t *testing.T) {
	t.Parallel()

	mod := New()

	if err := mod.OnDiscoveryReloaded(context.Background()); err != nil {
		t.Fatalf("OnDiscoveryReloaded error = %v", err)
	}
}

// TestModule_InitFromDiscovery_AllDisappearClearsList verifies the contract
// that even when the filtered list is empty, the module's datasource state is
// updated. Without this, an already-running module whose datasources have all
// disappeared would keep reporting them through DatasourceInfo/SandboxEnv.
func TestModule_InitFromDiscovery_AllDisappearClearsList(t *testing.T) {
	t.Parallel()

	mod := New()

	if err := mod.InitFromDiscovery([]types.DatasourceInfo{
		{Type: "clickhouse", Name: "clickhouse-raw"},
	}); err != nil {
		t.Fatalf("initial InitFromDiscovery error = %v", err)
	}

	if got := len(mod.DatasourceInfo()); got != 1 {
		t.Fatalf("after initial init: DatasourceInfo() has %d entries, want 1", got)
	}

	err := mod.InitFromDiscovery([]types.DatasourceInfo{})
	if !errors.Is(err, module.ErrNoValidConfig) {
		t.Fatalf("after all-disappear: err = %v, want ErrNoValidConfig", err)
	}

	if got := len(mod.DatasourceInfo()); got != 0 {
		t.Fatalf("after all-disappear: DatasourceInfo() has %d entries, want 0", got)
	}
}

// TestSchemaClient_RefreshEmptyDatasourcesClearsStale verifies that when the
// schema client refreshes with no datasources, any previously cached schemas
// are dropped — so queries don't keep reporting clusters that no longer exist.
func TestSchemaClient_RefreshEmptyDatasourcesClearsStale(t *testing.T) {
	t.Parallel()

	log := logrus.New()
	log.SetOutput(io.Discard)

	client := &clickhouseSchemaClient{
		log: log.WithField("test", "true"),
		clusters: map[string]*ClusterTables{
			"clickhouse-raw": {ClusterName: "clickhouse-raw", Tables: map[string]*TableSchema{"t": {Name: "t"}}},
		},
		datasources: map[string]string{},
		refreshNow:  make(chan struct{}, 1),
	}

	if err := client.refresh(context.Background()); err != nil {
		t.Fatalf("refresh error = %v", err)
	}

	if got := len(client.GetAllTables()); got != 0 {
		t.Fatalf("after empty refresh: GetAllTables() = %d, want 0", got)
	}
}

type fakeSchemaClient struct {
	lastUpdate []SchemaDiscoveryDatasource
}

func (f *fakeSchemaClient) Start(_ context.Context) error           { return nil }
func (f *fakeSchemaClient) Stop() error                             { return nil }
func (f *fakeSchemaClient) GetAllTables() map[string]*ClusterTables { return nil }
func (f *fakeSchemaClient) GetClusterTables(_ string) (*ClusterTables, bool) {
	return nil, false
}
func (f *fakeSchemaClient) GetTableInCluster(_, _, _ string) (*TableSchema, bool) {
	return nil, false
}
func (f *fakeSchemaClient) UpdateDatasources(d []SchemaDiscoveryDatasource) {
	f.lastUpdate = d
}
