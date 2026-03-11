package module

import "testing"

func TestLoadExampleCatalogNormalizesQueries(t *testing.T) {
	t.Parallel()

	raw := []byte(`
queries:
  name: Queries
  examples:
    - name: demo
      query: |
        SELECT 1
`)

	catalog, err := LoadExampleCatalog(raw, "demo")
	if err != nil {
		t.Fatalf("LoadExampleCatalog() error = %v", err)
	}

	if got := catalog["queries"].Examples[0].Query; got != "SELECT 1" {
		t.Fatalf("normalized query = %q, want %q", got, "SELECT 1")
	}
}

func TestLoadExampleCatalogReturnsParseErrors(t *testing.T) {
	t.Parallel()

	if _, err := LoadExampleCatalog([]byte(":\n  - bad"), "broken"); err == nil {
		t.Fatal("LoadExampleCatalog() error = nil, want parse failure")
	}
}
