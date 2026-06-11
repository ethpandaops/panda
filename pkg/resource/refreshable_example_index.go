package resource

import (
	"fmt"
	"sync"
)

// RefreshableExampleIndex wraps an ExampleIndex behind a swap so a background
// refresher can replace it (e.g. when proxy discovery changes which datasets
// are active in this deployment) without disrupting in-flight searches. It implements the same
// Search signature as ExampleIndex.
type RefreshableExampleIndex struct {
	mu  sync.RWMutex
	idx *ExampleIndex
}

// NewRefreshableExampleIndex wraps an initial index.
func NewRefreshableExampleIndex(idx *ExampleIndex) *RefreshableExampleIndex {
	return &RefreshableExampleIndex{idx: idx}
}

// Search delegates to the current index.
func (r *RefreshableExampleIndex) Search(query string, limit int) ([]SearchResult, error) {
	r.mu.RLock()
	idx := r.idx
	r.mu.RUnlock()

	if idx == nil {
		return nil, fmt.Errorf("example index not ready")
	}

	return idx.Search(query, limit)
}

// Swap replaces the current index. The previous index is dropped (not closed):
// the embedder it references is shared with the other search indices and is
// owned by the runtime, which closes it once at shutdown.
func (r *RefreshableExampleIndex) Swap(idx *ExampleIndex) {
	r.mu.Lock()
	r.idx = idx
	r.mu.Unlock()
}
