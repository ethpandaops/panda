package module

import (
	"fmt"

	"github.com/ethpandaops/panda/pkg/types"
)

// EnsureExampleCatalogLoaded populates the destination catalog exactly once.
func EnsureExampleCatalogLoaded(
	dst *map[string]types.ExampleCategory,
	loader ExampleCatalogLoader,
) error {
	if *dst != nil {
		return nil
	}

	examples, err := loader()
	if err != nil {
		return err
	}

	*dst = examples

	return nil
}

// ValidateUniqueDatasources validates datasource names and rejects duplicates.
func ValidateUniqueDatasources(datasources []types.DatasourceInfo) error {
	names := make(map[string]struct{}, len(datasources))

	for i, ds := range datasources {
		if ds.Name == "" {
			return fmt.Errorf("datasource[%d].name is required", i)
		}

		if _, exists := names[ds.Name]; exists {
			return fmt.Errorf("datasource[%d].name %q is duplicated", i, ds.Name)
		}

		names[ds.Name] = struct{}{}
	}

	return nil
}
