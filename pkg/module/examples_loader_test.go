package module

import (
	"strings"
	"testing"
)

func TestExampleCatalogLoaderReturnsDeepClones(t *testing.T) {
	t.Parallel()

	loader := NewExampleCatalogLoader([]byte(`
queries:
  name: Queries
  description: demo
  examples:
    - name: First
      description: desc
      query: "  SELECT 1  "
`), "demo")

	first, err := loader()
	if err != nil {
		t.Fatalf("first load error = %v", err)
	}

	second, err := loader()
	if err != nil {
		t.Fatalf("second load error = %v", err)
	}

	firstCategory := first["queries"]
	firstCategory.Examples[0].Name = "mutated"
	firstCategory.Examples[0].Query = "SELECT 2"
	first["queries"] = firstCategory

	secondCategory := second["queries"]
	if secondCategory.Examples[0].Name != "First" {
		t.Fatalf("second load example name = %q, want %q", secondCategory.Examples[0].Name, "First")
	}

	if secondCategory.Examples[0].Query != "SELECT 1" {
		t.Fatalf("second load example query = %q, want %q", secondCategory.Examples[0].Query, "SELECT 1")
	}
}

func TestLoadExampleCatalogTrimsQueries(t *testing.T) {
	t.Parallel()

	catalog, err := LoadExampleCatalog([]byte(`
queries:
  name: Queries
  description: demo
  examples:
    - name: First
      description: desc
      query: |
        SELECT 1

`), "demo")
	if err != nil {
		t.Fatalf("LoadExampleCatalog() error = %v", err)
	}

	got := catalog["queries"].Examples[0].Query
	if strings.Contains(got, "\n\n") {
		t.Fatalf("query = %q, want normalized whitespace", got)
	}

	if got != "SELECT 1" {
		t.Fatalf("query = %q, want %q", got, "SELECT 1")
	}
}
