// Package datasets ships dataset knowledge packs that describe how to query
// datasets stored in a generic transport (currently ClickHouse). A dataset is a
// named body of data (e.g. xatu-raw, xatu-cbt, otel-logs); its pack is content
// only — examples and getting-started guidance — and ships co-versioned with the
// release. The generic transport modules hold no dataset-specific knowledge.
package datasets

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/types"
)

// Compile-time interface checks.
var (
	_ module.Module                        = (*Module)(nil)
	_ module.DefaultEnabled                = (*Module)(nil)
	_ module.ProxyDiscoverable             = (*Module)(nil)
	_ module.ExamplesProvider              = (*Module)(nil)
	_ module.GettingStartedSnippetProvider = (*Module)(nil)
)

//go:embed */manifest.yaml */examples.yaml */getting-started.md
var packFS embed.FS

type manifest struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

type pack struct {
	name           string
	description    string
	examples       map[string]types.ExampleCategory
	gettingStarted string
}

// Module contributes dataset knowledge packs (examples + getting-started) to the
// registry. It owns no transport; the generic ClickHouse module executes the
// queries these packs describe. Packs are scoped to the datasets a deployment
// declares via proxy `contains`; when no deployment declares any, all packs are
// shown (back-compatible).
type Module struct {
	packs []pack

	mu     sync.RWMutex
	loaded bool
	// active is the set of dataset names declared by discovered datasources. A
	// nil/empty set means "show all packs".
	active map[string]bool
}

// New creates a new datasets module.
func New() *Module { return &Module{} }

func (m *Module) Name() string { return "datasets" }

// Init loads the embedded packs. The module takes no configuration. With no
// proxy discovery, all packs are exposed.
func (m *Module) Init(_ []byte) error { return m.ensureLoaded() }

// ApplyDefaults is a no-op; packs are static content.
func (m *Module) ApplyDefaults() {}

// Validate is a no-op; embedded packs are validated at load time.
func (m *Module) Validate() error { return nil }

// Start is a no-op.
func (m *Module) Start(_ context.Context) error { return nil }

// Stop is a no-op.
func (m *Module) Stop(_ context.Context) error { return nil }

// DefaultEnabled activates the module without configuration: packs ship with the
// release and are always available.
func (m *Module) DefaultEnabled() bool { return true }

// InitFromDiscovery scopes the exposed packs to the datasets declared by
// discovered datasources (via their Contents bindings). Always returns nil: the
// packs ship with the release, so the module is always active. When no
// datasource declares any dataset, the active set stays empty and all packs are
// shown.
func (m *Module) InitFromDiscovery(datasources []types.DatasourceInfo) error {
	if err := m.ensureLoaded(); err != nil {
		return err
	}

	active := make(map[string]bool)

	for _, ds := range datasources {
		for _, b := range ds.Contents {
			if b.Dataset != "" {
				active[b.Dataset] = true
			}
		}
	}

	m.mu.Lock()
	m.active = active
	m.mu.Unlock()

	return nil
}

func (m *Module) ensureLoaded() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.loaded {
		return nil
	}

	entries, err := packFS.ReadDir(".")
	if err != nil {
		return fmt.Errorf("reading dataset packs: %w", err)
	}

	packs := make([]pack, 0, len(entries))

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		p, err := loadPack(entry.Name())
		if err != nil {
			return fmt.Errorf("loading dataset pack %q: %w", entry.Name(), err)
		}

		packs = append(packs, p)
	}

	sort.Slice(packs, func(i, j int) bool { return packs[i].name < packs[j].name })

	m.packs = packs
	m.loaded = true

	return nil
}

func loadPack(dir string) (pack, error) {
	p := pack{name: dir}

	manifestBytes, err := packFS.ReadFile(dir + "/manifest.yaml")
	if err != nil {
		return p, fmt.Errorf("reading manifest: %w", err)
	}

	var mf manifest
	if err := yaml.Unmarshal(manifestBytes, &mf); err != nil {
		return p, fmt.Errorf("parsing manifest: %w", err)
	}

	if mf.Name != "" {
		p.name = mf.Name
	}

	p.description = mf.Description

	exampleBytes, err := packFS.ReadFile(dir + "/examples.yaml")
	if err != nil {
		return p, fmt.Errorf("reading examples: %w", err)
	}

	if err := yaml.Unmarshal(exampleBytes, &p.examples); err != nil {
		return p, fmt.Errorf("parsing examples: %w", err)
	}

	gsBytes, err := packFS.ReadFile(dir + "/getting-started.md")
	if err != nil {
		return p, fmt.Errorf("reading getting-started: %w", err)
	}

	p.gettingStarted = string(gsBytes)

	return p, nil
}

// activePacks returns the packs to expose, scoped to the discovered active set
// (or all packs when the set is empty).
func (m *Module) activePacks() []pack {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.active) == 0 {
		return m.packs
	}

	out := make([]pack, 0, len(m.packs))
	for _, p := range m.packs {
		if m.active[p.name] {
			out = append(out, p)
		}
	}

	return out
}

// Examples aggregates query examples across the active packs. Categories that
// appear in more than one pack (e.g. a category split across xatu-raw and
// xatu-cbt) are merged so no examples are dropped.
func (m *Module) Examples() map[string]types.ExampleCategory {
	result := make(map[string]types.ExampleCategory)

	for _, p := range m.activePacks() {
		for key, cat := range p.examples {
			existing, ok := result[key]
			if !ok {
				result[key] = cat

				continue
			}

			existing.Examples = append(existing.Examples, cat.Examples...)
			result[key] = existing
		}
	}

	return result
}

// GettingStartedSnippet concatenates the per-pack getting-started guidance for
// the active packs.
func (m *Module) GettingStartedSnippet() string {
	var b strings.Builder

	for _, p := range m.activePacks() {
		if p.gettingStarted == "" {
			continue
		}

		if b.Len() > 0 {
			b.WriteString("\n")
		}

		b.WriteString(p.gettingStarted)
	}

	return b.String()
}
