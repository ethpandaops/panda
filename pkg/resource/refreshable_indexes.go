package resource

import (
	"fmt"
	"sync"
)

// RefreshableRunbookIndex wraps a RunbookIndex behind an atomic swap so the
// runtime can replace it (e.g. after the proxy's embedding model changes and the
// corpus is re-embedded) without disrupting in-flight searches. While the inner
// index is nil — during a re-index — Search returns a not-ready error rather than
// scoring a new-model query against an old-model index.
type RefreshableRunbookIndex struct {
	mu  sync.RWMutex
	idx *RunbookIndex
}

// NewRefreshableRunbookIndex wraps an initial index.
func NewRefreshableRunbookIndex(idx *RunbookIndex) *RefreshableRunbookIndex {
	return &RefreshableRunbookIndex{idx: idx}
}

// Search delegates to the current index, or reports not-ready while swapped out.
func (r *RefreshableRunbookIndex) Search(query string, limit int) ([]RunbookSearchResult, error) {
	r.mu.RLock()
	idx := r.idx
	r.mu.RUnlock()

	if idx == nil {
		return nil, fmt.Errorf("runbook index not ready")
	}

	return idx.Search(query, limit)
}

// Swap replaces the current index. Passing nil parks the index as not-ready
// (used at the start of a re-index so no search mixes embedding model spaces).
func (r *RefreshableRunbookIndex) Swap(idx *RunbookIndex) {
	r.mu.Lock()
	r.idx = idx
	r.mu.Unlock()
}

// RefreshableEIPIndex wraps an EIPIndex behind an atomic swap. See
// RefreshableRunbookIndex for the rationale.
type RefreshableEIPIndex struct {
	mu  sync.RWMutex
	idx *EIPIndex
}

// NewRefreshableEIPIndex wraps an initial index.
func NewRefreshableEIPIndex(idx *EIPIndex) *RefreshableEIPIndex {
	return &RefreshableEIPIndex{idx: idx}
}

// Search delegates to the current index, or reports not-ready while swapped out.
func (r *RefreshableEIPIndex) Search(query string, limit int) ([]EIPSearchResult, error) {
	r.mu.RLock()
	idx := r.idx
	r.mu.RUnlock()

	if idx == nil {
		return nil, fmt.Errorf("EIP index not ready")
	}

	return idx.Search(query, limit)
}

// Swap replaces the current index (nil parks it as not-ready).
func (r *RefreshableEIPIndex) Swap(idx *EIPIndex) {
	r.mu.Lock()
	r.idx = idx
	r.mu.Unlock()
}

// RefreshableConsensusSpecIndex wraps a ConsensusSpecIndex behind an atomic swap.
// It delegates both the vector spec search and the lexical constant search.
type RefreshableConsensusSpecIndex struct {
	mu  sync.RWMutex
	idx *ConsensusSpecIndex
}

// NewRefreshableConsensusSpecIndex wraps an initial index.
func NewRefreshableConsensusSpecIndex(idx *ConsensusSpecIndex) *RefreshableConsensusSpecIndex {
	return &RefreshableConsensusSpecIndex{idx: idx}
}

// SearchSpecs delegates to the current index, or reports not-ready while swapped
// out (spec search is vector-based and must not mix model spaces).
func (r *RefreshableConsensusSpecIndex) SearchSpecs(query string, limit int) ([]ConsensusSpecSearchResult, error) {
	r.mu.RLock()
	idx := r.idx
	r.mu.RUnlock()

	if idx == nil {
		return nil, fmt.Errorf("consensus spec index not ready")
	}

	return idx.SearchSpecs(query, limit)
}

// SearchConstants delegates to the current index. Constant search is lexical (no
// embedding), so it returns nil — not an error — while the index is swapped out.
func (r *RefreshableConsensusSpecIndex) SearchConstants(query string, limit int) []ConstantSearchResult {
	r.mu.RLock()
	idx := r.idx
	r.mu.RUnlock()

	if idx == nil {
		return nil
	}

	return idx.SearchConstants(query, limit)
}

// Swap replaces the current index (nil parks it as not-ready).
func (r *RefreshableConsensusSpecIndex) Swap(idx *ConsensusSpecIndex) {
	r.mu.Lock()
	r.idx = idx
	r.mu.Unlock()
}
