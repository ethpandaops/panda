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
	_ module.Module                        = (*Module)(nil)
	_ module.DefaultEnabled                = (*Module)(nil)
	_ module.ProxyDiscoverable             = (*Module)(nil)
	_ module.SchemaResolverAware           = (*Module)(nil)
	_ module.ExamplesProvider              = (*Module)(nil)
	_ module.GettingStartedSnippetProvider = (*Module)(nil)
)

//go:embed */manifest.yaml */examples.yaml */getting-started.md
var packFS embed.FS

type manifest struct {
	Name string `yaml:"name"`
}

type pack struct {
	name           string
	examples       map[string]types.ExampleCategory
	gettingStarted string
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
	// resolver validates example table references against the live schema.
	// When nil, examples are not schema-validated.
	resolver module.SchemaResolver
	// warnedUnknown dedupes warnings about declared datasets with no matching
	// pack, so the periodic discovery refresh doesn't repeat them.
	warnedUnknown map[string]bool
	// droppedLogged dedupes warnings about examples hidden by live-schema
	// validation, so repeated Examples() calls don't repeat them.
	droppedLogged map[string]bool
}

// New creates a new datasets module.
func New() *Module {
	return &Module{
		log:           logrus.WithField("module", "datasets"),
		warnedUnknown: make(map[string]bool, 4),
		droppedLogged: make(map[string]bool, 16),
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

// SetSchemaResolver injects the live-schema resolver used to validate example
// table references. Safe to call before or after the schema is populated.
func (m *Module) SetSchemaResolver(resolver module.SchemaResolver) {
	m.mu.Lock()
	m.resolver = resolver
	m.mu.Unlock()
}

// InitFromDiscovery scopes the exposed packs to the datasets declared by
// discovered datasources (via their Contents bindings). Always returns nil: the
// packs ship with the release, so the module is always active. When no
// datasource declares any dataset, the active set stays empty and all packs are
// shown.
func (m *Module) InitFromDiscovery(datasources []types.DatasourceInfo) error {
	if err := m.ensureLoaded(); err != nil {
		return err
	}

	active := make(map[string]bool, 4)

	for _, ds := range datasources {
		for _, b := range ds.Contents {
			if b.Dataset != "" {
				active[b.Dataset] = true
			}
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
		m.log.Warn("No declared dataset matches a shipped knowledge pack; falling back to all packs")
	}

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

// activePacks returns the packs to expose, scoped to the discovered active set.
// When the set is empty (no declarations) or matches no shipped pack (typo, or
// a release older than the declared dataset), all packs are exposed: showing
// broad guidance degrades better than showing none.
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

	if len(out) == 0 {
		return m.packs
	}

	return out
}

// Examples aggregates query examples across the active packs. Categories that
// appear in more than one pack (e.g. a category split across xatu-raw and
// xatu-cbt) are merged so no examples are dropped. When a live-schema resolver
// is available, examples that reference tables absent from the live schema are
// dropped so stale guidance is never surfaced.
func (m *Module) Examples() map[string]types.ExampleCategory {
	validate := m.exampleValidator()

	result := make(map[string]types.ExampleCategory)

	for _, p := range m.activePacks() {
		for key, cat := range p.examples {
			kept := cat.Examples
			if validate != nil {
				kept = make([]types.Example, 0, len(cat.Examples))
				for _, ex := range cat.Examples {
					missing := validate(ex)
					if len(missing) == 0 {
						kept = append(kept, ex)

						continue
					}

					m.logDropped(ex, missing)
				}
			}

			if len(kept) == 0 {
				continue
			}

			existing, ok := result[key]
			if !ok {
				cat.Examples = kept
				result[key] = cat

				continue
			}

			// Merge into a fresh slice: kept may alias the pack's stored slice,
			// and appending in place would write into the pack's backing array.
			merged := make([]types.Example, 0, len(existing.Examples)+len(kept))
			merged = append(merged, existing.Examples...)
			merged = append(merged, kept...)
			existing.Examples = merged
			result[key] = existing
		}
	}

	return result
}

// exampleValidator returns a function reporting which of an example's table
// references are absent from the live schema (empty = keep the example). It
// returns nil when no resolver is set (validation disabled). Per-target
// known-table sets are cached for the duration of the call; a target with no
// schema yet (ok=false) is not validated.
func (m *Module) exampleValidator() func(types.Example) []string {
	m.mu.RLock()
	resolver := m.resolver
	m.mu.RUnlock()

	if resolver == nil {
		return nil
	}

	type lookup struct {
		known   map[string]bool
		present bool
	}

	cache := make(map[string]lookup, 4)

	return func(ex types.Example) []string {
		l, ok := cache[ex.Target]
		if !ok {
			known, present := resolver.KnownTables(ex.Target)
			l = lookup{known: known, present: present}
			cache[ex.Target] = l
		}

		if !l.present {
			return nil
		}

		return unknownTableRefs(ex.Query, l.known)
	}
}

// logDropped warns (once per example+reason) that live-schema validation hid an
// example, so drift fails loud rather than silently narrowing guidance.
func (m *Module) logDropped(ex types.Example, missing []string) {
	key := ex.Target + "\x00" + ex.Name + "\x00" + strings.Join(missing, ",")

	m.mu.Lock()
	logged := m.droppedLogged[key]
	m.droppedLogged[key] = true
	m.mu.Unlock()

	if logged {
		return
	}

	m.log.WithFields(logrus.Fields{
		"example":        ex.Name,
		"target":         ex.Target,
		"missing_tables": missing,
	}).Warn("Hiding example: it references tables absent from the live schema (likely schema drift)")
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
