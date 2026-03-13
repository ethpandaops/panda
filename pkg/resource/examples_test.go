package resource

import (
	"context"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/types"
)

func TestCreateExamplesHandlerMarshalsExamples(t *testing.T) {
	t.Parallel()

	moduleReg := newInitializedModuleRegistry(t, &testModule{
		name: "examples",
		examples: map[string]types.ExampleCategory{
			"queries": {
				Name:        "Queries",
				Description: "Starter queries",
				Examples: []types.Example{
					{Name: "Head blocks", Description: "List latest blocks", Query: "SELECT 1"},
				},
			},
		},
	})

	content, err := createExamplesHandler(moduleReg)(context.Background(), "examples://queries")
	if err != nil {
		t.Fatalf("createExamplesHandler() error = %v", err)
	}

	var decoded map[string]types.ExampleCategory
	decodeJSON(t, content, &decoded)

	if got := decoded["queries"].Examples[0].Query; got != "SELECT 1" {
		t.Fatalf("queries example query = %q, want SELECT 1", got)
	}
}

func TestRegisterExamplesResourcesAddsStaticResource(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(logrus.New())
	moduleReg := newInitializedModuleRegistry(t, &testModule{name: "examples"})

	RegisterExamplesResources(logrus.New(), reg, moduleReg)

	static := reg.ListStatic()
	if len(static) != 1 {
		t.Fatalf("ListStatic() len = %d, want 1", len(static))
	}

	resource := static[0]
	if resource.URI != "examples://queries" {
		t.Fatalf("resource URI = %q, want examples://queries", resource.URI)
	}

	if resource.Name != "Query Examples" {
		t.Fatalf("resource name = %q, want Query Examples", resource.Name)
	}

	if resource.MIMEType != "application/json" {
		t.Fatalf("resource MIMEType = %q, want application/json", resource.MIMEType)
	}

	if description := resource.Description; !strings.Contains(description, "ClickHouse") || !strings.Contains(description, "Loki") {
		t.Fatalf("resource description = %q, want datasource guidance", description)
	}
}
