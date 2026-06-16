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
		types.Example{Name: "x", Dataset: "xatu-raw"},
		types.Example{Name: "y", Dataset: "xatu-cbt"},
	)
	// Same content, different example order: signature must match (order-independent).
	b := cats(
		types.Example{Name: "y", Dataset: "xatu-cbt"},
		types.Example{Name: "x", Dataset: "xatu-raw"},
	)

	if exampleSignature(a) != exampleSignature(b) {
		t.Fatal("signature should be independent of example order")
	}
}

func TestExampleSignatureChanges(t *testing.T) {
	base := exampleSignature(cats(types.Example{Name: "x", Dataset: "xatu-raw"}))

	added := exampleSignature(cats(
		types.Example{Name: "x", Dataset: "xatu-raw"},
		types.Example{Name: "z", Dataset: "xatu-raw"},
	))
	if base == added {
		t.Error("adding an example should change the signature")
	}

	redataseted := exampleSignature(cats(types.Example{Name: "x", Dataset: "xatu-cbt"}))
	if base == redataseted {
		t.Error("moving an example between datasets should change the signature")
	}

	removed := exampleSignature(cats())
	if base == removed {
		t.Error("removing an example should change the signature")
	}
}
