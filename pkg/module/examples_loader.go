package module

import (
	"fmt"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/ethpandaops/panda/pkg/types"
)

// ExampleCatalogLoader loads an embedded example catalog and returns a fresh
// immutable clone on each call.
type ExampleCatalogLoader func() (map[string]types.ExampleCategory, error)

// NewExampleCatalogLoader returns an explicit once-backed loader for an
// embedded example catalog.
func NewExampleCatalogLoader(raw []byte, moduleName string) ExampleCatalogLoader {
	var (
		once    sync.Once
		catalog map[string]types.ExampleCategory
		err     error
	)

	return func() (map[string]types.ExampleCategory, error) {
		once.Do(func() {
			catalog, err = LoadExampleCatalog(raw, moduleName)
		})
		if err != nil {
			return nil, err
		}

		return CloneExampleCatalog(catalog), nil
	}
}

// LoadExampleCatalog parses an embedded examples YAML payload into an immutable
// catalog with normalized query strings.
func LoadExampleCatalog(raw []byte, moduleName string) (map[string]types.ExampleCategory, error) {
	catalog := make(map[string]types.ExampleCategory)
	if err := yaml.Unmarshal(raw, &catalog); err != nil {
		return nil, fmt.Errorf("parsing %s examples: %w", moduleName, err)
	}

	for key, category := range catalog {
		for i := range category.Examples {
			category.Examples[i].Query = strings.TrimSpace(category.Examples[i].Query)
		}

		catalog[key] = category
	}

	return catalog, nil
}

// CloneExampleCatalog returns a deep copy so callers cannot mutate shared state.
func CloneExampleCatalog(catalog map[string]types.ExampleCategory) map[string]types.ExampleCategory {
	if catalog == nil {
		return nil
	}

	cloned := make(map[string]types.ExampleCategory, len(catalog))
	for key, category := range catalog {
		category.Examples = append([]types.Example(nil), category.Examples...)
		cloned[key] = category
	}

	return cloned
}
