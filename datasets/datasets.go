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

	"gopkg.in/yaml.v3"

	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/types"
)

// Compile-time interface checks.
var (
	_ module.Module                        = (*Module)(nil)
	_ module.DefaultEnabled                = (*Module)(nil)
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
// queries these packs describe.
type Module struct {
	packs []pack
}

// New creates a new datasets module.
func New() *Module { return &Module{} }

func (m *Module) Name() string { return "datasets" }

// Init loads the embedded packs. The module takes no configuration.
func (m *Module) Init(_ []byte) error { return m.load() }

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

func (m *Module) load() error {
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

// Examples aggregates query examples across all packs. Categories that appear in
// more than one pack (e.g. a category split across xatu-raw and xatu-cbt) are
// merged so no examples are dropped.
func (m *Module) Examples() map[string]types.ExampleCategory {
	result := make(map[string]types.ExampleCategory)

	for _, p := range m.packs {
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

// GettingStartedSnippet concatenates the per-pack getting-started guidance.
func (m *Module) GettingStartedSnippet() string {
	var b strings.Builder

	for _, p := range m.packs {
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
