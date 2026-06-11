package datasets

import (
	"context"
	"encoding/json"
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

	list, err := m.handleDatasetsList(context.Background(), "datasets://list")
	if err != nil {
		t.Fatalf("handleDatasetsList() error = %v", err)
	}

	var parsed struct {
		Datasets []datasetSummary `json:"datasets"`
	}
	if err := json.Unmarshal([]byte(list), &parsed); err != nil {
		t.Fatalf("parsing datasets list: %v", err)
	}

	for _, d := range parsed.Datasets {
		if want := d.Name == "otel-logs"; d.Active != want {
			t.Errorf("dataset %q active = %v, want %v", d.Name, d.Active, want)
		}
	}
}

func TestInitFromDiscoveryUnknownDatasetsShowNothing(t *testing.T) {
	m := New()

	// The deployment declares only a dataset this release has no pack for
	// (typo, or a dataset newer than this release). The declaration is
	// authoritative: surfacing other packs would be guidance known to be wrong
	// for this deployment.
	err := m.InitFromDiscovery([]types.DatasourceInfo{
		{
			Type: "clickhouse",
			Name: "clickhouse-raw",
			Contents: []types.DatasetBinding{
				{Dataset: "xatu_raw"},
			},
		},
	})
	if err != nil {
		t.Fatalf("InitFromDiscovery() error = %v", err)
	}

	if ex := m.Examples(); len(ex) != 0 {
		t.Fatalf("unknown-only declaration: got %d example categories, want 0", len(ex))
	}

	if packs := m.activePacks(); len(packs) != 0 {
		t.Fatalf("unknown-only declaration: %d packs exposed, want 0", len(packs))
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

func TestExamplesStampedWithDataset(t *testing.T) {
	m := newLoaded(t)

	for key, cat := range m.Examples() {
		for _, ex := range cat.Examples {
			if ex.Dataset == "" {
				t.Fatalf("category %q example %q has no dataset stamp", key, ex.Name)
			}
		}
	}
}

func TestDatasetDetailResource(t *testing.T) {
	m := newLoaded(t)

	if err := m.InitFromDiscovery([]types.DatasourceInfo{
		{
			Type: "clickhouse",
			Name: "clickhouse-raw",
			Contents: []types.DatasetBinding{
				{Dataset: "otel-logs", Params: map[string]string{"database": "external"}, Notes: "hosted logs"},
			},
		},
	}); err != nil {
		t.Fatalf("InitFromDiscovery() error = %v", err)
	}

	out, err := m.handleDatasetDetail(context.Background(), "datasets://otel-logs")
	if err != nil {
		t.Fatalf("handleDatasetDetail() error = %v", err)
	}

	for _, want := range []string{
		"# otel-logs —",
		"clickhouse-raw",
		"database=external",
		"hosted logs",
		"{db}.otel_logs",
		`search(type="examples", dataset="otel-logs"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dataset guide missing %q", want)
		}
	}

	if _, err := m.handleDatasetDetail(context.Background(), "datasets://nope"); err == nil {
		t.Error("expected error for unknown dataset")
	}
}

func TestDatasetDetailResourceCLIContext(t *testing.T) {
	m := newLoaded(t)

	ctx := types.WithClientContext(context.Background(), types.ClientContextCLI)
	out, err := m.handleDatasetDetail(ctx, "datasets://otel-logs")
	if err != nil {
		t.Fatalf("handleDatasetDetail() error = %v", err)
	}

	if !strings.Contains(out, `panda search examples --dataset otel-logs "<topic>"`) {
		t.Errorf("CLI dataset guide missing search command, got:\n%s", out)
	}

	if !strings.Contains(out, `panda resources datasources://clickhouse`) {
		t.Errorf("CLI dataset guide missing resource command, got:\n%s", out)
	}

	if strings.Contains(out, `search(type="examples"`) {
		t.Errorf("CLI dataset guide should not use MCP tool-call syntax, got:\n%s", out)
	}

	if !strings.Contains(out, "compatibility mode") || !strings.Contains(out, "Target") {
		t.Errorf("CLI dataset guide should explain missing placement metadata, got:\n%s", out)
	}

	if strings.Contains(out, "No datasource in this deployment declares this dataset") {
		t.Errorf("CLI dataset guide should not imply absence when no placement metadata was advertised, got:\n%s", out)
	}
}

func TestDatasetsListResource(t *testing.T) {
	m := newLoaded(t)

	out, err := m.handleDatasetsList(context.Background(), "datasets://list")
	if err != nil {
		t.Fatalf("handleDatasetsList() error = %v", err)
	}

	for _, want := range []string{"xatu-raw", "xatu-cbt", "otel-logs", `"active": true`} {
		if !strings.Contains(out, want) {
			t.Errorf("datasets list missing %q", want)
		}
	}

	if !strings.Contains(out, "has not advertised dataset placement metadata") {
		t.Errorf("datasets list should explain missing placement metadata, got:\n%s", out)
	}
}
