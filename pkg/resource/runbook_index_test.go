package resource

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ethpandaops/panda/pkg/types"
	"github.com/sirupsen/logrus"
)

func TestNewRunbookIndexAndSearch(t *testing.T) {
	t.Parallel()

	runbooks := []types.Runbook{
		{
			Name:        "Investigate Finality",
			Description: "Check consensus lag",
			Tags:        []string{"finality", "consensus"},
			Content:     "Look at finality checkpoints first.",
		},
		{
			Name:        "Inspect Execution Payloads",
			Description: "Check block production",
			Tags:        []string{"execution"},
			Content:     "Review builder logs.",
		},
	}

	embedder := stubEmbedder{
		vectors: map[string][]float32{
			buildRunbookSearchText(runbooks[0]): {1, 0},
			buildRunbookSearchText(runbooks[1]): {0, 1},
			"finality":                          {1, 0},
		},
	}

	idx, err := NewRunbookIndex(logrus.New(), embedder, runbooks)
	if err != nil {
		t.Fatalf("NewRunbookIndex() error = %v", err)
	}

	results, err := idx.Search("finality", 1)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Search() len = %d, want 1", len(results))
	}
	if got := results[0].Runbook.Name; got != "Investigate Finality" {
		t.Fatalf("Search() top runbook = %q, want Investigate Finality", got)
	}
}

func TestRunbookIndexValidatesEmbedderAndLimit(t *testing.T) {
	t.Parallel()

	if _, err := NewRunbookIndex(logrus.New(), nil, nil); err == nil || err.Error() != "embedder is required" {
		t.Fatalf("NewRunbookIndex(nil) error = %v, want embedder is required", err)
	}

	idx := &RunbookIndex{
		embedder: stubEmbedder{
			vectors: map[string][]float32{
				"finality": {1},
			},
		},
	}

	results, err := idx.Search("finality", 0)
	if err != nil {
		t.Fatalf("Search(limit=0) error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("Search(limit=0) len = %d, want 0", len(results))
	}

	if _, err := (*RunbookIndex)(nil).Search("finality", 1); err == nil || err.Error() != "runbook index is not initialized" {
		t.Fatalf("nil Search() error = %v, want runbook index is not initialized", err)
	}
}

func TestBuildRunbookSearchTextIncludesOverviewBeforeSectionBreak(t *testing.T) {
	t.Parallel()

	text := buildRunbookSearchText(types.Runbook{
		Name:        "Investigate Finality",
		Description: "Check consensus lag",
		Tags:        []string{"finality", "consensus"},
		Content:     "Start here.\nKeep reading.\n## Deep dive\nIgnore this.",
	})

	for _, needle := range []string{"Investigate Finality", "Check consensus lag", "finality consensus", "Start here. Keep reading."} {
		if !strings.Contains(text, needle) {
			t.Fatalf("buildRunbookSearchText() = %q, want %q", text, needle)
		}
	}

	if strings.Contains(text, "Ignore this.") {
		t.Fatalf("buildRunbookSearchText() should stop at section break, got %q", text)
	}
}

func TestExtractOverviewStopsAtCodeFenceAndRespectsMaxLength(t *testing.T) {
	t.Parallel()

	overview := extractOverview("Line one\nLine two\n```python\nprint(1)\n```", 300)
	if overview != "Line one Line two" {
		t.Fatalf("extractOverview(code fence) = %q, want Line one Line two", overview)
	}

	truncated := extractOverview("a long first sentence that should exceed the limit", 8)
	if len(truncated) <= 8 {
		t.Fatalf("extractOverview(limit) = %q, want content exceeding limit threshold", truncated)
	}
}

func TestRunbookIndexCloseAndJSONTags(t *testing.T) {
	t.Parallel()

	idx := &RunbookIndex{
		embedder: stubEmbedder{vectors: map[string][]float32{"finality": {1}}},
		runbooks: []indexedRunbook{
			{Runbook: types.Runbook{Name: "Investigate Finality"}, Vector: []float32{1, 0}},
		},
	}

	if err := idx.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if idx.embedder != nil || idx.runbooks != nil {
		t.Fatalf("Close() did not clear state: %#v", idx)
	}

	payload, err := json.Marshal(RunbookSearchResult{
		Runbook: types.Runbook{Name: "Investigate Finality"},
		Score:   0.91,
	})
	if err != nil {
		t.Fatalf("json.Marshal(RunbookSearchResult) error = %v", err)
	}

	if got := string(payload); !strings.Contains(got, `"similarity_score":0.91`) {
		t.Fatalf("json = %s, want similarity_score field", got)
	}
}
