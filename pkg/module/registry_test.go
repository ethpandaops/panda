package module

import (
	"context"
	"testing"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/types"
)

type baseTestExtension struct {
	name string
}

func (e *baseTestExtension) Name() string                  { return e.name }
func (e *baseTestExtension) Init(_ []byte) error           { return nil }
func (e *baseTestExtension) ApplyDefaults()                {}
func (e *baseTestExtension) Validate() error               { return nil }
func (e *baseTestExtension) Start(_ context.Context) error { return nil }
func (e *baseTestExtension) Stop(_ context.Context) error  { return nil }

type sandboxEnvTestExtension struct {
	baseTestExtension
	env map[string]string
}

func (e *sandboxEnvTestExtension) SandboxEnv() (map[string]string, error) {
	return e.env, nil
}

type examplesTestExtension struct {
	baseTestExtension
	examples map[string]types.ExampleCategory
}

func (e *examplesTestExtension) Examples() map[string]types.ExampleCategory {
	return e.examples
}

type docsTestExtension struct {
	baseTestExtension
	docs map[string]types.ModuleDoc
}

func (e *docsTestExtension) PythonAPIDocs() map[string]types.ModuleDoc {
	return e.docs
}

type datasourceTestExtension struct {
	baseTestExtension
	infos []types.DatasourceInfo
}

func (e *datasourceTestExtension) DatasourceInfo() []types.DatasourceInfo {
	return e.infos
}

type gettingStartedTestExtension struct {
	baseTestExtension
	snippet string
}

func (e *gettingStartedTestExtension) GettingStartedSnippet() string {
	return e.snippet
}

func TestRegistryCapabilityAggregation(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(logrus.New())

	reg.Add(&baseTestExtension{name: "base"})
	reg.Add(&sandboxEnvTestExtension{
		baseTestExtension: baseTestExtension{name: "sandbox"},
		env:               map[string]string{"A": "1"},
	})
	reg.Add(&examplesTestExtension{
		baseTestExtension: baseTestExtension{name: "examples"},
		examples: map[string]types.ExampleCategory{
			"queries": {Name: "Queries"},
		},
	})
	reg.Add(&docsTestExtension{
		baseTestExtension: baseTestExtension{name: "docs"},
		docs: map[string]types.ModuleDoc{
			"demo": {Description: "demo docs"},
		},
	})
	reg.Add(&datasourceTestExtension{
		baseTestExtension: baseTestExtension{name: "datasource"},
		infos:             []types.DatasourceInfo{{Type: "custom", Name: "demo"}},
	})
	reg.Add(&gettingStartedTestExtension{
		baseTestExtension: baseTestExtension{name: "snippet"},
		snippet:           "hello world",
	})

	for _, name := range reg.All() {
		if err := reg.InitModule(name, nil); err != nil {
			t.Fatalf("InitModule(%q) error = %v", name, err)
		}
	}

	env, err := reg.SandboxEnv()
	if err != nil {
		t.Fatalf("SandboxEnv() error = %v", err)
	}
	if env["A"] != "1" || len(env) != 1 {
		t.Fatalf("SandboxEnv() = %#v, want only sandbox capability values", env)
	}

	examples := reg.Examples()
	if _, ok := examples["queries"]; !ok || len(examples) != 1 {
		t.Fatalf("Examples() = %#v, want single capability contribution", examples)
	}

	docs := reg.PythonAPIDocs()
	if docs["demo"].Description != "demo docs" || len(docs) != 1 {
		t.Fatalf("PythonAPIDocs() = %#v, want single capability contribution", docs)
	}

	infos := reg.DatasourceInfo()
	if len(infos) != 1 || infos[0].Name != "demo" {
		t.Fatalf("DatasourceInfo() = %#v, want single capability contribution", infos)
	}

	snippets := reg.GettingStartedSnippets()
	if snippets != "hello world\n" {
		t.Fatalf("GettingStartedSnippets() = %q, want %q", snippets, "hello world\n")
	}
}
