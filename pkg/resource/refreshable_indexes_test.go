package resource

import "testing"

// The refreshable wrappers are parked (Swap(nil)) at the start of a re-index so
// that no search dot-products a new-model query against an old-model index while
// the corpus is being re-embedded. These tests pin that contract: a parked index
// reports not-ready rather than returning (stale) results.

func TestRefreshableRunbookIndex_ParkedReportsNotReady(t *testing.T) {
	idx := NewRefreshableRunbookIndex(nil)
	if _, err := idx.Search("query", 5); err == nil {
		t.Fatal("expected not-ready error when inner index is nil")
	}

	idx.Swap(nil)

	if _, err := idx.Search("query", 5); err == nil {
		t.Fatal("expected not-ready error after Swap(nil)")
	}
}

func TestRefreshableEIPIndex_ParkedReportsNotReady(t *testing.T) {
	idx := NewRefreshableEIPIndex(nil)
	if _, err := idx.Search("query", 5); err == nil {
		t.Fatal("expected not-ready error when inner index is nil")
	}

	idx.Swap(nil)

	if _, err := idx.Search("query", 5); err == nil {
		t.Fatal("expected not-ready error after Swap(nil)")
	}
}

func TestRefreshableConsensusSpecIndex_ParkedReportsNotReady(t *testing.T) {
	idx := NewRefreshableConsensusSpecIndex(nil)

	// Vector spec search must report not-ready while parked.
	if _, err := idx.SearchSpecs("query", 5); err == nil {
		t.Fatal("expected not-ready error for SearchSpecs when parked")
	}

	// Constant search is lexical (no embedding), so it returns nil — not an
	// error — while parked.
	if got := idx.SearchConstants("query", 5); got != nil {
		t.Fatalf("expected nil constants while parked, got %v", got)
	}
}
