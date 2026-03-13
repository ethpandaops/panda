package runbooks

import (
	"testing"

	"github.com/sirupsen/logrus"
)

func TestRegistryAccessors(t *testing.T) {
	t.Parallel()

	reg, err := NewRegistry(logrus.New())
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}

	if reg.Count() == 0 {
		t.Fatal("Count() = 0, want loaded runbooks")
	}

	all := reg.All()
	if len(all) != reg.Count() {
		t.Fatalf("All() len = %d, want %d", len(all), reg.Count())
	}

	first := all[0]
	if got := reg.Get(first.Name); got == nil || got.Name != first.Name {
		t.Fatalf("Get(%q) = %#v, want matching runbook", first.Name, got)
	}

	if got := reg.Get("missing"); got != nil {
		t.Fatalf("Get(missing) = %#v, want nil", got)
	}

	all[0].Name = "mutated"
	if got := reg.Get(first.Name); got == nil || got.Name != first.Name {
		t.Fatalf("All() should return a copy, Get(%q) = %#v", first.Name, got)
	}

	tags := reg.Tags()
	if len(tags) == 0 {
		t.Fatal("Tags() = nil/empty, want runbook tags")
	}
}
