package datasets

import (
	"strings"
	"testing"

	"github.com/ethpandaops/panda/pkg/types"
)

func newLoaded(t *testing.T) *Module {
	t.Helper()

	m := New()
	if err := m.Init(nil); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	return m
}

func TestExamplesPreserveTotalCount(t *testing.T) {
	m := newLoaded(t)

	total := 0
	for _, cat := range m.Examples() {
		total += len(cat.Examples)
	}

	// The packs were split from the original clickhouse examples.yaml (164 examples).
	if total != 164 {
		t.Fatalf("total examples = %d, want 164", total)
	}
}

func TestExamplesMergeSplitCategories(t *testing.T) {
	m := newLoaded(t)
	ex := m.Examples()

	// These categories were split across xatu-raw and xatu-cbt; aggregation must
	// merge them rather than let one pack clobber the other.
	wantMerged := map[string]int{
		"network_health": 15,
		"mev_analysis":   11,
		"blob_analysis":  9,
	}

	for key, want := range wantMerged {
		cat, ok := ex[key]
		if !ok {
			t.Fatalf("merged category %q missing", key)
		}

		if len(cat.Examples) != want {
			t.Errorf("category %q has %d examples, want %d", key, len(cat.Examples), want)
		}
	}
}

func TestInitFromDiscoveryScopesToDeclaredDatasets(t *testing.T) {
	m := New()

	// A deployment that only declares otel-logs should only surface otel-logs packs.
	err := m.InitFromDiscovery([]types.DatasourceInfo{
		{
			Type: "clickhouse",
			Name: "clickhouse-raw",
			Contents: []types.DatasetBinding{
				{Dataset: "otel-logs", Params: map[string]string{"database": "external"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("InitFromDiscovery() error = %v", err)
	}

	ex := m.Examples()

	total := 0
	for _, cat := range ex {
		total += len(cat.Examples)
	}

	if total != 6 {
		t.Fatalf("scoped to otel-logs: total examples = %d, want 6", total)
	}

	if _, ok := ex["block_timing"]; ok {
		t.Error("scoped to otel-logs but xatu-cbt category block_timing present")
	}

	gs := m.GettingStartedSnippet()
	if strings.Contains(gs, "xatu-raw") || strings.Contains(gs, "xatu-cbt") {
		t.Error("scoped to otel-logs but getting-started includes xatu packs")
	}
}

func TestInitFromDiscoveryNoBindingsShowsAll(t *testing.T) {
	m := New()

	// Discovery with datasources but no `contains` declarations (legacy) shows all packs.
	if err := m.InitFromDiscovery([]types.DatasourceInfo{
		{Type: "clickhouse", Name: "clickhouse-raw"},
	}); err != nil {
		t.Fatalf("InitFromDiscovery() error = %v", err)
	}

	total := 0
	for _, cat := range m.Examples() {
		total += len(cat.Examples)
	}

	if total != 164 {
		t.Fatalf("no bindings: total examples = %d, want 164 (all packs)", total)
	}
}

func TestGettingStartedCoversAllPacks(t *testing.T) {
	m := newLoaded(t)
	gs := m.GettingStartedSnippet()

	for _, want := range []string{"xatu-raw", "xatu-cbt", "otel-logs"} {
		if !strings.Contains(gs, want) {
			t.Errorf("getting-started snippet missing pack heading %q", want)
		}
	}
}
