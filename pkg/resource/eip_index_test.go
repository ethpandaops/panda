package resource

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/mcp/pkg/eips"
	"github.com/ethpandaops/mcp/pkg/embedding"
	"github.com/ethpandaops/mcp/pkg/types"
)

func TestEIPSearch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	modelPath := "../../models/MiniLM-L6-v2.Q8_0.gguf"
	if _, err := os.Stat(modelPath); err != nil {
		t.Skipf("embedding model not found at %s", modelPath)
	}

	log := logrus.New()
	log.SetLevel(logrus.InfoLevel)

	// Load EIPs.
	ctx := context.Background()

	reg, err := eips.NewRegistry(ctx, log, "")
	if err != nil {
		t.Fatalf("NewRegistry() error: %v", err)
	}

	t.Logf("Loaded %d EIPs", reg.Count())

	// Create embedder.
	embedder, err := embedding.New(modelPath, 0)
	if err != nil {
		t.Fatalf("embedding.New() error: %v", err)
	}

	defer func() { _ = embedder.Close() }()

	// Build index with vector caching.
	cachedVectors := reg.CachedVectors()

	idx, updatedVectors, err := NewEIPIndex(log, embedder, reg.All(), cachedVectors)
	if err != nil {
		t.Fatalf("NewEIPIndex() error: %v", err)
	}

	// Save vectors for next run.
	if err := reg.SaveVectors(updatedVectors); err != nil {
		t.Logf("Warning: failed to save vectors: %v", err)
	}

	// Search queries.
	queries := []string{
		"63/64 rule",
		"account abstraction",
		"blob transactions",
		"proof of stake",
	}

	for _, query := range queries {
		results, err := idx.Search(query, 5)
		if err != nil {
			t.Fatalf("Search(%q) error: %v", query, err)
		}

		fmt.Printf("\n=== %q ===\n", query)

		for _, r := range results {
			if r.Score < 0.25 {
				continue
			}

			fmt.Printf("  EIP-%d: %s (score: %.2f)\n", r.EIP.Number, r.EIP.Title, r.Score)
			fmt.Printf("    %s\n", r.EIP.Description)
			fmt.Printf("    Status: %s | Type: %s", r.EIP.Status, r.EIP.Type)

			if r.EIP.Category != "" {
				fmt.Printf(" | Category: %s", r.EIP.Category)
			}

			fmt.Println()
			fmt.Printf("    %s\n", r.EIP.URL)
		}
	}
}

func TestEIPSearchVectorReuse(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	modelPath := "../../models/MiniLM-L6-v2.Q8_0.gguf"
	if _, err := os.Stat(modelPath); err != nil {
		t.Skipf("embedding model not found at %s", modelPath)
	}

	log := logrus.New()
	log.SetLevel(logrus.InfoLevel)

	embedder, err := embedding.New(modelPath, 0)
	if err != nil {
		t.Fatalf("embedding.New() error: %v", err)
	}

	defer func() { _ = embedder.Close() }()

	testEIPs := []types.EIP{
		{Number: 1, Title: "EIP Purpose", Description: "Guidelines for EIP authors"},
		{Number: 2, Title: "Homestead", Description: "Homestead hard fork changes"},
		{Number: 3, Title: "Test", Description: "A test EIP"},
	}

	// First build: all embedded.
	_, vectors1, err := NewEIPIndex(log, embedder, testEIPs, nil)
	if err != nil {
		t.Fatalf("first build error: %v", err)
	}

	// Each short EIP produces 1 chunk → 3 total.
	if len(vectors1) != 3 {
		t.Fatalf("expected 3 vectors, got %d", len(vectors1))
	}

	// Second build with same EIPs: all reused.
	_, vectors2, err := NewEIPIndex(log, embedder, testEIPs, vectors1)
	if err != nil {
		t.Fatalf("second build error: %v", err)
	}

	// Vectors should be identical (reused).
	for i := range testEIPs {
		key := fmt.Sprintf("%d:0", testEIPs[i].Number)
		if vectors2[key].TextHash != vectors1[key].TextHash {
			t.Errorf("EIP-%d: hash mismatch after reuse", testEIPs[i].Number)
		}
	}

	// Third build: change one EIP's description.
	modifiedEIPs := make([]types.EIP, len(testEIPs))
	copy(modifiedEIPs, testEIPs)
	modifiedEIPs[2].Description = "A completely different description"

	_, vectors3, err := NewEIPIndex(log, embedder, modifiedEIPs, vectors2)
	if err != nil {
		t.Fatalf("third build error: %v", err)
	}

	// EIP-1 and EIP-2 should be reused, EIP-3 should be re-embedded.
	if vectors3["1:0"].TextHash != vectors2["1:0"].TextHash {
		t.Error("EIP-1 should have been reused")
	}

	if vectors3["2:0"].TextHash != vectors2["2:0"].TextHash {
		t.Error("EIP-2 should have been reused")
	}

	if vectors3["3:0"].TextHash == vectors2["3:0"].TextHash {
		t.Error("EIP-3 should have been re-embedded (description changed)")
	}
}
