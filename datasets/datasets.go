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

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"

	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/types"
)

// Compile-time interface checks.
var (
	_ module.Module            = (*Module)(nil)
	_ module.DefaultEnabled    = (*Module)(nil)
	_ module.ProxyDiscoverable = (*Module)(nil)
	_ module.ExamplesProvider  = (*Module)(nil)
	_ module.ResourceProvider  = (*Module)(nil)
)

//go:embed */manifest.yaml */examples.yaml */getting-started.md
var packFS embed.FS

type manifest struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

type pack struct {
	name        string
	description string
	// examples are stamped with Dataset = pack name at load time.
	examples       map[string]types.ExampleCategory
	gettingStarted string
}

// placement records that a discovered datasource declared this dataset.
type placement struct {
	Datasource string            `json:"datasource"`
	Params     map[string]string `json:"params,omitempty"`
	Notes      string            `json:"notes,omitempty"`
}

// Module contributes dataset knowledge packs (examples + getting-started) to the
// registry. It owns no transport; the generic ClickHouse module executes the
// queries these packs describe. Packs are scoped to the datasets a deployment
// declares via proxy `contains`; when no deployment declares any, all packs are
// shown (back-compatible).
type Module struct {
	log   logrus.FieldLogger
	packs []pack

	mu     sync.RWMutex
	loaded bool
	// active is the set of dataset names declared by discovered datasources. A
	// nil/empty set means "show all packs".
	active map[string]bool
	// placements maps dataset name -> the datasources that declared it, with
	// their params and notes. Rebuilt on every discovery refresh.
	placements map[string][]placement
	// warnedUnknown dedupes warnings about declared datasets with no matching
	// pack, so the periodic discovery refresh doesn't repeat them.
	warnedUnknown map[string]bool
}

// New creates a new datasets module.
func New() *Module {
	return &Module{
		log:           logrus.WithField("module", "datasets"),
		warnedUnknown: make(map[string]bool, 4),
	}
}

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
// discovered datasources (via their Contents bindings) and records where each
// dataset lives. Always returns nil: the packs ship with the release, so the
// module is always active. When no datasource declares any dataset, the active
// set stays empty and all packs are shown.
func (m *Module) InitFromDiscovery(datasources []types.DatasourceInfo) error {
	if err := m.ensureLoaded(); err != nil {
		return err
	}

	active := make(map[string]bool, 4)
	placements := make(map[string][]placement, 4)

	for _, ds := range datasources {
		for _, b := range ds.Contents {
			if b.Dataset == "" {
				continue
			}

			active[b.Dataset] = true
			placements[b.Dataset] = append(placements[b.Dataset], placement{
				Datasource: ds.Name,
				Params:     b.Params,
				Notes:      b.Notes,
			})
		}
	}

	m.mu.Lock()

	known := make(map[string]bool, len(m.packs))
	for _, p := range m.packs {
		known[p.name] = true
	}

	matched := 0

	for name := range active {
		if known[name] {
			matched++

			continue
		}

		if !m.warnedUnknown[name] {
			m.warnedUnknown[name] = true
			m.log.WithField("dataset", name).
				Warn("Proxy declares a dataset with no matching knowledge pack in this release; check for a typo or upgrade panda")
		}
	}

	if len(active) > 0 && matched == 0 {
		m.log.Warn("No declared dataset matches a shipped knowledge pack; no dataset guidance will be surfaced — fix the proxy contains declarations or upgrade panda")
	}

	m.active = active
	m.placements = placements
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

	if mf.Description == "" {
		return p, fmt.Errorf("manifest description is required (rendered as the pack's section heading)")
	}

	p.description = mf.Description

	exampleBytes, err := packFS.ReadFile(dir + "/examples.yaml")
	if err != nil {
		return p, fmt.Errorf("reading examples: %w", err)
	}

	if err := yaml.Unmarshal(exampleBytes, &p.examples); err != nil {
		return p, fmt.Errorf("parsing examples: %w", err)
	}

	// Stamp pack identity on every example so downstream consumers (search
	// filtering, result display) know which dataset an example belongs to.
	for key, cat := range p.examples {
		for i := range cat.Examples {
			cat.Examples[i].Dataset = p.name
		}

		p.examples[key] = cat
	}

	gsBytes, err := packFS.ReadFile(dir + "/getting-started.md")
	if err != nil {
		return p, fmt.Errorf("reading getting-started: %w", err)
	}

	p.gettingStarted = strings.TrimSpace(string(gsBytes))

	return p, nil
}

// activePacks returns the packs to expose, scoped to the discovered active set.
// An empty set means no deployment declared anything (old proxy) — with no
// information, all packs are exposed. A non-empty set is authoritative even
// when nothing matches: the deployment said what it contains, and surfacing
// packs for other datasets would hand the assistant guidance that is known to
// be wrong here.
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

// packPlacements returns where the named dataset lives in this deployment.
func (m *Module) packPlacements(name string) []placement {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.placements[name]
}

// Examples aggregates query examples across the active packs. Categories that
// appear in more than one pack (e.g. a category split across xatu-raw and
// xatu-cbt) are merged so no examples are dropped. Examples are never filtered
// against the live schema: a stale example fails loudly at query time where
// the agent can see and recover from it, whereas serve-time hiding is
// invisible and amplifies any discovery or parsing bug into silently missing
// guidance.
func (m *Module) Examples() map[string]types.ExampleCategory {
	result := make(map[string]types.ExampleCategory)

	for _, p := range m.activePacks() {
		for key, cat := range p.examples {
			existing, ok := result[key]
			if !ok {
				result[key] = cat

				continue
			}

			// Merge into a fresh slice: appending in place would write into
			// the first pack's backing array.
			merged := make([]types.Example, 0, len(existing.Examples)+len(cat.Examples))
			merged = append(merged, existing.Examples...)
			merged = append(merged, cat.Examples...)
			existing.Examples = merged
			result[key] = existing
		}
	}

	return result
}
