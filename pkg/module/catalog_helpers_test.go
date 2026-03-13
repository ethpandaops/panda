package module

import (
	"errors"
	"testing"

	"github.com/ethpandaops/panda/pkg/types"
)

func TestEnsureExampleCatalogLoaded(t *testing.T) {
	t.Parallel()

	examples := map[string]types.ExampleCategory{
		"queries": {Name: "Queries"},
	}
	loadCalls := 0

	var catalog map[string]types.ExampleCategory
	err := EnsureExampleCatalogLoaded(&catalog, func() (map[string]types.ExampleCategory, error) {
		loadCalls++
		return CloneExampleCatalog(examples), nil
	})
	if err != nil {
		t.Fatalf("EnsureExampleCatalogLoaded() error = %v", err)
	}

	if loadCalls != 1 || catalog["queries"].Name != "Queries" {
		t.Fatalf("EnsureExampleCatalogLoaded() = (%d, %#v), want one load and populated catalog", loadCalls, catalog)
	}

	err = EnsureExampleCatalogLoaded(&catalog, func() (map[string]types.ExampleCategory, error) {
		loadCalls++
		return nil, nil
	})
	if err != nil {
		t.Fatalf("EnsureExampleCatalogLoaded(second call) error = %v", err)
	}

	if loadCalls != 1 {
		t.Fatalf("EnsureExampleCatalogLoaded() loadCalls = %d, want 1", loadCalls)
	}

	var missing map[string]types.ExampleCategory
	if err := EnsureExampleCatalogLoaded(&missing, func() (map[string]types.ExampleCategory, error) {
		return nil, errors.New("boom")
	}); err == nil || err.Error() != "boom" {
		t.Fatalf("EnsureExampleCatalogLoaded(error) = %v, want boom", err)
	}
}

func TestValidateUniqueDatasources(t *testing.T) {
	t.Parallel()

	if err := ValidateUniqueDatasources([]types.DatasourceInfo{{Name: "xatu"}, {Name: "cbt"}}); err != nil {
		t.Fatalf("ValidateUniqueDatasources(valid) error = %v", err)
	}

	if err := ValidateUniqueDatasources([]types.DatasourceInfo{{Name: ""}}); err == nil {
		t.Fatal("ValidateUniqueDatasources(missing name) error = nil, want error")
	}

	if err := ValidateUniqueDatasources([]types.DatasourceInfo{{Name: "xatu"}, {Name: "xatu"}}); err == nil {
		t.Fatal("ValidateUniqueDatasources(duplicate) error = nil, want error")
	}
}
