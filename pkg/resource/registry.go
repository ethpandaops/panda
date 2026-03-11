// Package resource provides MCP resource handlers for domain knowledge.
package resource

import (
	"context"
	"fmt"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/types"
)

// ReadHandler is a function that reads a resource and returns its content.
type ReadHandler = types.ReadHandler

// StaticResource represents a static resource with a fixed URI.
type StaticResource = types.StaticResource

// TemplateResource represents a resource template with URI parameters.
type TemplateResource = types.TemplateResource

// Registry manages MCP resources and their handlers.
type Registry interface {
	// RegisterStatic registers a static resource with a fixed URI.
	RegisterStatic(res types.StaticResource)

	// RegisterTemplate registers a template resource with URI parameters.
	RegisterTemplate(res types.TemplateResource)

	// ListStatic returns all registered static resources.
	ListStatic() []mcp.Resource

	// ListTemplates returns all registered resource templates.
	ListTemplates() []mcp.ResourceTemplate

	// Read reads a resource by URI and returns its content, mime type, and any error.
	Read(ctx context.Context, uri string) (content string, mimeType string, err error)
}

// registry is the default implementation of Registry.
type registry struct {
	log       logrus.FieldLogger
	mu        sync.RWMutex
	static    []types.StaticResource
	templates []types.TemplateResource
}

// NewRegistry creates a new resource registry.
func NewRegistry(log logrus.FieldLogger) Registry {
	return &registry{
		log:       log.WithField("component", "resource_registry"),
		static:    make([]types.StaticResource, 0, 8),
		templates: make([]types.TemplateResource, 0, 4),
	}
}

// RegisterStatic registers a static resource with a fixed URI.
func (r *registry) RegisterStatic(res types.StaticResource) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.static = append(r.static, res)
	r.log.WithField("uri", res.Resource.URI).Debug("Registered static resource")
}

// RegisterTemplate registers a template resource with URI parameters.
func (r *registry) RegisterTemplate(res types.TemplateResource) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.templates = append(r.templates, res)

	templateURI := ""
	if res.Template.URITemplate != nil {
		templateURI = res.Template.URITemplate.Raw()
	}

	r.log.WithField("template", templateURI).Debug("Registered template resource")
}

// ListStatic returns all registered static resources.
func (r *registry) ListStatic() []mcp.Resource {
	r.mu.RLock()
	defer r.mu.RUnlock()

	resources := make([]mcp.Resource, len(r.static))
	for i, s := range r.static {
		resources[i] = s.Resource
	}

	return resources
}

// ListTemplates returns all registered resource templates.
func (r *registry) ListTemplates() []mcp.ResourceTemplate {
	r.mu.RLock()
	defer r.mu.RUnlock()

	templates := make([]mcp.ResourceTemplate, len(r.templates))
	for i, t := range r.templates {
		templates[i] = t.Template
	}

	return templates
}

// Read reads a resource by URI and returns its content and mime type.
func (r *registry) Read(ctx context.Context, uri string) (string, string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	r.log.WithField("uri", uri).Debug("Reading resource")

	// Check static resources first
	for _, s := range r.static {
		if s.Resource.URI == uri {
			content, err := s.Handler(ctx, uri)
			if err != nil {
				return "", "", fmt.Errorf("reading static resource %s: %w", uri, err)
			}

			return content, s.Resource.MIMEType, nil
		}
	}

	// Check template resources
	for _, t := range r.templates {
		if t.Pattern.MatchString(uri) {
			content, err := t.Handler(ctx, uri)
			if err != nil {
				return "", "", fmt.Errorf("reading template resource %s: %w", uri, err)
			}

			return content, t.Template.MIMEType, nil
		}
	}

	return "", "", fmt.Errorf("unknown resource URI: %s", uri)
}

// Compile-time check that registry implements Registry.
var _ Registry = (*registry)(nil)
