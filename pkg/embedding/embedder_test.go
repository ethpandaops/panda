package embedding

import "testing"

func TestNewRequiresModelPath(t *testing.T) {
	t.Parallel()

	embedder, err := New("")
	if err == nil {
		t.Fatal("New(\"\") error = nil, want error")
	}

	if err.Error() != "model path is required" {
		t.Fatalf("New(\"\") error = %q, want model path is required", err)
	}

	if embedder != nil {
		t.Fatalf("New(\"\") embedder = %#v, want nil", embedder)
	}
}

func TestCloseHandlesNilAndZeroValueEmbedder(t *testing.T) {
	t.Parallel()

	var nilEmbedder *Embedder
	if err := nilEmbedder.Close(); err != nil {
		t.Fatalf("nil Close() error = %v, want nil", err)
	}

	embedder := &Embedder{}
	if err := embedder.Close(); err != nil {
		t.Fatalf("zero-value Close() error = %v, want nil", err)
	}
}
