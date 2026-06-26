package runbooks

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/types"
)

// Registry holds loaded runbooks and provides access for indexing and search.
type Registry struct {
	log      logrus.FieldLogger
	runbooks []types.Runbook
	byName   map[string]*types.Runbook
	byRef    map[string]*types.Runbook
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
	byRef := make(map[string]*types.Runbook, len(runbooks))
	for i := range runbooks {
		byName[runbooks[i].Name] = &runbooks[i]
		if ref := RefKey(runbooks[i]); ref != "" {
			byRef[ref] = &runbooks[i]
		}
	}

	log.WithField("runbook_count", len(runbooks)).Info("Runbook registry loaded")

	return &Registry{
		log:      log,
		runbooks: runbooks,
		byName:   byName,
		byRef:    byRef,
	}, nil
}

// RefURI returns the stable runbooks:// URI used by search results and readers.
func RefURI(rb types.Runbook) string {
	key := RefKey(rb)
	if key == "" {
		return ""
	}

	return "runbooks://" + key
}

// RefKey returns the stable URI key for a runbook. Embedded runbooks use the
// markdown filename stem so refs remain shell-safe and independent of title text.
func RefKey(rb types.Runbook) string {
	filePath := strings.TrimSpace(rb.FilePath)
	if filePath != "" {
		base := filepath.Base(filePath)
		ext := filepath.Ext(base)
		if ext != "" {
			base = strings.TrimSuffix(base, ext)
		}
		if key := slugRefKey(base); key != "" {
			return key
		}
	}

	return slugRefKey(rb.Name)
}

func slugRefKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))

	var b strings.Builder
	lastSep := false

	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			lastSep = false
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			lastSep = false
		case r == '-' || r == '_':
			if b.Len() > 0 && !lastSep {
				b.WriteRune(r)
				lastSep = true
			}
		default:
			if b.Len() > 0 && !lastSep {
				b.WriteByte('_')
				lastSep = true
			}
		}
	}

	return strings.Trim(b.String(), "-_")
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

// GetByRef returns a runbook by its stable ref key, or nil if not found.
func (r *Registry) GetByRef(ref string) *types.Runbook {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.byRef[ref]
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
