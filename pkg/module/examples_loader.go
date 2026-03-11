package module

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ethpandaops/panda/pkg/types"
)

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
