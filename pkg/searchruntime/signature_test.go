package searchruntime

import (
	"testing"

	"github.com/ethpandaops/panda/pkg/types"
)

func cats(examples ...types.Example) map[string]types.ExampleCategory {
	return map[string]types.ExampleCategory{
		"cat": {Name: "cat", Examples: examples},
	}
}

func TestExampleSignatureStable(t *testing.T) {
	a := cats(
		types.Example{Name: "x", Target: "clickhouse-raw"},
		types.Example{Name: "y", Target: "clickhouse-refined"},
	)
	// Same content, different example order: signature must match (order-independent).
	b := cats(
		types.Example{Name: "y", Target: "clickhouse-refined"},
		types.Example{Name: "x", Target: "clickhouse-raw"},
	)

	if exampleSignature(a) != exampleSignature(b) {
		t.Fatal("signature should be independent of example order")
	}
}

func TestExampleSignatureChanges(t *testing.T) {
	base := exampleSignature(cats(types.Example{Name: "x", Target: "clickhouse-raw"}))

	added := exampleSignature(cats(
		types.Example{Name: "x", Target: "clickhouse-raw"},
		types.Example{Name: "z", Target: "clickhouse-raw"},
	))
	if base == added {
		t.Error("adding an example should change the signature")
	}

	retargeted := exampleSignature(cats(types.Example{Name: "x", Target: "clickhouse-refined"}))
	if base == retargeted {
		t.Error("re-targeting an example should change the signature")
	}

	removed := exampleSignature(cats())
	if base == removed {
		t.Error("removing an example should change the signature")
	}
}
