package datasets

import (
	"strings"
	"testing"
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

func TestGettingStartedCoversAllPacks(t *testing.T) {
	m := newLoaded(t)
	gs := m.GettingStartedSnippet()

	for _, want := range []string{"xatu-raw", "xatu-cbt", "otel-logs"} {
		if !strings.Contains(gs, want) {
			t.Errorf("getting-started snippet missing pack heading %q", want)
		}
	}
}
