package resource

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sirupsen/logrus"
)

func TestRegistryListReturnsSnapshots(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(logrus.New())
	reg.RegisterStatic(StaticResource{
		Resource: mcp.NewResource("examples://queries", "Examples"),
		Handler:  func(_ context.Context, _ string) (string, error) { return "{}", nil },
	})
	reg.RegisterTemplate(TemplateResource{
		Template: mcp.NewResourceTemplate("networks://{name}", "Network Details"),
		Pattern:  networkURIPattern,
		Handler:  func(_ context.Context, _ string) (string, error) { return "{}", nil },
	})

	static := reg.ListStatic()
	templates := reg.ListTemplates()

	static[0].URI = "changed://uri"
	templates[0].Name = "Changed"

	staticAgain := reg.ListStatic()
	templatesAgain := reg.ListTemplates()

	if staticAgain[0].URI != "examples://queries" {
		t.Fatalf("ListStatic() returned shared slice, got URI %q", staticAgain[0].URI)
	}

	if templatesAgain[0].Name != "Network Details" {
		t.Fatalf("ListTemplates() returned shared slice, got Name %q", templatesAgain[0].Name)
	}
}

func TestRegistryReadPrefersStaticResourcesOverTemplates(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(logrus.New())
	reg.RegisterStatic(StaticResource{
		Resource: mcp.NewResource("networks://all", "Static Networks", mcp.WithMIMEType("text/plain")),
		Handler:  func(_ context.Context, _ string) (string, error) { return "static", nil },
	})
	reg.RegisterTemplate(TemplateResource{
		Template: mcp.NewResourceTemplate("networks://{name}", "Template Networks", mcp.WithTemplateMIMEType("application/json")),
		Pattern:  networkURIPattern,
		Handler:  func(_ context.Context, _ string) (string, error) { return `{"kind":"template"}`, nil },
	})

	content, mimeType, err := reg.Read(context.Background(), "networks://all")
	if err != nil {
		t.Fatalf("Read(networks://all) error = %v", err)
	}

	if content != "static" || mimeType != "text/plain" {
		t.Fatalf("Read(networks://all) = (%q, %q), want static text/plain", content, mimeType)
	}
}
