package resource

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sirupsen/logrus"
)

func TestCreateGettingStartedHandlerOmitsTemplatesSectionWhenEmpty(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(logrus.New())
	reg.RegisterStatic(StaticResource{
		Resource: mcp.NewResource("python://ethpandaops", "API", mcp.WithMIMEType("application/json")),
		Handler:  func(_ context.Context, _ string) (string, error) { return "{}", nil },
	})

	moduleReg := newInitializedModuleRegistry(t, &testModule{
		name:    "starter",
		snippet: "### Module setup\n\nUse this first.\n",
	})

	content, err := createGettingStartedHandler(reg, testToolLister{
		tools: []mcp.Tool{
			mcp.NewTool("execute_python", mcp.WithDescription("Run Python snippets\nwith more detail")),
			mcp.NewTool("search", mcp.WithDescription(" Search docs ")),
		},
	}, moduleReg)(context.Background(), "ethpandaops://getting-started")
	if err != nil {
		t.Fatalf("createGettingStartedHandler() error = %v", err)
	}

	if !strings.Contains(content, "### Module setup") {
		t.Fatalf("content missing module snippet:\n%s", content)
	}

	if !strings.Contains(content, "- **execute_python**: Run Python snippets") {
		t.Fatalf("content missing trimmed execute_python description:\n%s", content)
	}

	if !strings.Contains(content, "- **search**: Search docs") {
		t.Fatalf("content missing trimmed search description:\n%s", content)
	}

	if strings.Contains(content, "**Templates:**") {
		t.Fatalf("content unexpectedly contains templates section:\n%s", content)
	}
}

func TestRegisterGettingStartedResourcesAddsGuideResource(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(logrus.New())

	RegisterGettingStartedResources(logrus.New(), reg, testToolLister{}, newInitializedModuleRegistry(t, &testModule{name: "starter"}))

	static := reg.ListStatic()
	if len(static) != 1 {
		t.Fatalf("ListStatic() len = %d, want 1", len(static))
	}

	resource := static[0]
	if resource.URI != "ethpandaops://getting-started" {
		t.Fatalf("resource URI = %q, want ethpandaops://getting-started", resource.URI)
	}

	if resource.Name != "Getting Started Guide" {
		t.Fatalf("resource name = %q, want Getting Started Guide", resource.Name)
	}

	if resource.MIMEType != "text/markdown" {
		t.Fatalf("resource MIMEType = %q, want text/markdown", resource.MIMEType)
	}
}
