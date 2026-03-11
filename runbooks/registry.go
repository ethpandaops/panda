package runbooks

import (
	"fmt"
	"sync"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/types"
)

// Registry holds loaded runbooks and provides access for indexing and search.
type Registry struct {
	log      logrus.FieldLogger
	runbooks []types.Runbook
	byName   map[string]*types.Runbook
	mu       sync.RWMutex
}

// NewRegistry creates a new runbook registry and loads all embedded runbooks.
func NewRegistry(log logrus.FieldLogger) (*Registry, error) {
	log = log.WithField("component", "runbook_registry")

	runbooks, err := Load()
	if err != nil {
		return nil, fmt.Errorf("loading runbooks: %w", err)
	}

	byName := make(map[string]*types.Runbook, len(runbooks))
	for i := range runbooks {
		byName[runbooks[i].Name] = &runbooks[i]
	}

	log.WithField("runbook_count", len(runbooks)).Info("Runbook registry loaded")

	return &Registry{
		log:      log,
		runbooks: runbooks,
		byName:   byName,
	}, nil
}

// All returns all loaded runbooks.
func (r *Registry) All() []types.Runbook {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Return a copy to prevent external mutation.
	result := make([]types.Runbook, len(r.runbooks))
	copy(result, r.runbooks)

	return result
}

// Get returns a runbook by name, or nil if not found.
func (r *Registry) Get(name string) *types.Runbook {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.byName[name]
}

// Count returns the number of loaded runbooks.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return len(r.runbooks)
}

// Tags returns all unique tags across all runbooks.
func (r *Registry) Tags() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tagSet := make(map[string]struct{})
	for _, rb := range r.runbooks {
		for _, tag := range rb.Tags {
			tagSet[tag] = struct{}{}
		}
	}

	tags := make([]string, 0, len(tagSet))
	for tag := range tagSet {
		tags = append(tags, tag)
	}

	return tags
}
