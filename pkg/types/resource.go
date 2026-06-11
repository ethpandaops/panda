package types

import (
	"context"
	"regexp"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/ethpandaops/panda/pkg/surface"
)

// ReadHandler handles reading a resource by URI for a given client surface.
type ReadHandler func(ctx context.Context, uri string, s surface.Dialect) (string, error)

// StaticResource is a resource with a fixed URI.
type StaticResource struct {
	Resource mcp.Resource
	Handler  ReadHandler
}

// TemplateResource is a resource with a URI pattern.
type TemplateResource struct {
	Template mcp.ResourceTemplate
	Pattern  *regexp.Regexp
	Handler  ReadHandler
}
