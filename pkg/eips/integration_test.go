package eips

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sirupsen/logrus"

	pkgtypes "github.com/ethpandaops/mcp/pkg/types"
)

func testLog() logrus.FieldLogger {
	l := logrus.New()
	l.SetLevel(logrus.DebugLevel)

	return l
}

func TestFetchAndParse(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx := context.Background()
	f := newFetcher(testLog())

	// Test: get latest commit SHA.
	sha, err := f.latestCommitSHA(ctx)
	if err != nil {
		t.Fatalf("latestCommitSHA() error: %v", err)
	}

	t.Logf("Latest commit SHA: %s", sha)

	if len(sha) < 40 {
		t.Fatalf("SHA too short: %q", sha)
	}

	// Test: fetch and parse all EIPs.
	eips, err := f.fetchAll(ctx)
	if err != nil {
		t.Fatalf("fetchAll() error: %v", err)
	}

	t.Logf("Fetched %d EIPs", len(eips))

	if len(eips) < 500 {
		t.Fatalf("Expected at least 500 EIPs, got %d", len(eips))
	}

	// Spot check a well-known EIP.
	var found bool

	for _, eip := range eips {
		if eip.Number == 1559 {
			found = true
			t.Logf("EIP-1559: %s", eip.Title)
			t.Logf("  Status: %s, Type: %s, Category: %s", eip.Status, eip.Type, eip.Category)
			t.Logf("  URL: %s", eip.URL)

			if eip.Title == "" {
				t.Error("EIP-1559 has empty title")
			}

			if eip.Status == "" {
				t.Error("EIP-1559 has empty status")
			}

			break
		}
	}

	if !found {
		t.Error("EIP-1559 not found in results")
	}
}

func TestRegistryWithCache(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx := context.Background()
	cacheDir := filepath.Join(t.TempDir(), "eip-cache")
	log := testLog()

	// First load: fetches from GitHub.
	reg, err := NewRegistry(ctx, log, cacheDir)
	if err != nil {
		t.Fatalf("NewRegistry() error: %v", err)
	}

	count := reg.Count()
	t.Logf("Registry loaded %d EIPs", count)

	if count < 500 {
		t.Fatalf("Expected at least 500 EIPs, got %d", count)
	}

	// Verify cache was written.
	cachePath := filepath.Join(cacheDir, "eips.json")

	info, err := os.Stat(cachePath)
	if err != nil {
		t.Fatalf("Cache file not found: %v", err)
	}

	t.Logf("Cache file size: %d bytes", info.Size())

	// Second load: should hit cache.
	reg2, err := NewRegistry(ctx, log, cacheDir)
	if err != nil {
		t.Fatalf("NewRegistry() from cache error: %v", err)
	}

	if reg2.Count() != count {
		t.Errorf("Cache returned %d EIPs, expected %d", reg2.Count(), count)
	}

	// Verify filter methods.
	statuses := reg.Statuses()
	categories := reg.Categories()
	types := reg.Types()

	t.Logf("Statuses: %v", statuses)
	t.Logf("Categories: %v", categories)
	t.Logf("Types: %v", types)

	if len(statuses) < 3 {
		t.Errorf("Expected at least 3 statuses, got %d", len(statuses))
	}

	if len(categories) < 2 {
		t.Errorf("Expected at least 2 categories, got %d", len(categories))
	}
}

func TestVectorCachePersistence(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx := context.Background()
	cacheDir := filepath.Join(t.TempDir(), "eip-cache")
	log := testLog()

	reg, err := NewRegistry(ctx, log, cacheDir)
	if err != nil {
		t.Fatalf("NewRegistry() error: %v", err)
	}

	// Initially no vectors cached.
	vecs := reg.CachedVectors()
	if len(vecs) != 0 {
		t.Errorf("Expected 0 cached vectors initially, got %d", len(vecs))
	}

	// Simulate index builder saving vectors for a few chunks.
	fakeVectors := map[string]pkgtypes.EIPVector{
		"1559:0": {TextHash: "abc123", Vector: []float32{0.1, 0.2, 0.3}},
		"4844:0": {TextHash: "def456", Vector: []float32{0.4, 0.5, 0.6}},
	}

	if err := reg.SaveVectors(fakeVectors); err != nil {
		t.Fatalf("SaveVectors() error: %v", err)
	}

	// Reload registry from cache — vectors should persist.
	reg2, err := NewRegistry(ctx, log, cacheDir)
	if err != nil {
		t.Fatalf("NewRegistry() reload error: %v", err)
	}

	vecs2 := reg2.CachedVectors()
	if len(vecs2) != 2 {
		t.Fatalf("Expected 2 cached vectors after reload, got %d", len(vecs2))
	}

	if vecs2["1559:0"].TextHash != "abc123" {
		t.Errorf("EIP-1559 chunk 0 TextHash = %q, want %q", vecs2["1559:0"].TextHash, "abc123")
	}

	if len(vecs2["4844:0"].Vector) != 3 {
		t.Errorf("EIP-4844 chunk 0 vector length = %d, want 3", len(vecs2["4844:0"].Vector))
	}

	t.Logf("Vector cache round-trip: OK (%d vectors persisted)", len(vecs2))
}
