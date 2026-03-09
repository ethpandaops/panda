// Package extension defines the Extension interface and registry for
// datasource extensions that extend the MCP server.
package extension

import (
	"context"
	"errors"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/mcp/pkg/types"
)

// ErrNoValidConfig indicates that an extension was configured but has no valid
// entries (e.g., all clusters/instances have empty required fields).
// This is not an error - the extension should be skipped gracefully.
var ErrNoValidConfig = errors.New("no valid configuration entries")

// CartographoorAware is an optional interface that extensions can implement
// to receive the cartographoor client for network discovery.
// The client parameter is passed as any to avoid circular imports;
// extensions should type-assert to cartographoor.CartographoorClient.
type CartographoorAware interface {
	SetCartographoorClient(client any)
}

// ProxyAware is an optional interface that extensions can implement
// to receive the proxy service for proxy-backed operations.
// The client parameter is passed as any to avoid circular imports;
// extensions should type-assert to proxy.Service.
type ProxyAware interface {
	SetProxyClient(client any)
}

// DefaultEnabled is an optional interface that extensions can implement
// to indicate they should be initialized even without explicit config.
// This is useful for extensions like dora that work with discovered data
// and require no user configuration.
type DefaultEnabled interface {
	// DefaultEnabled returns true if the extension should be initialized
	// without explicit config in the config file.
	DefaultEnabled() bool
}

// EnabledAware is an optional interface for extensions that can be
// initialized but still disabled via config.
type EnabledAware interface {
	Enabled() bool
}

// ResourceRegistry is the interface extensions use to register MCP resources.
// This avoids a circular dependency between extension and resource packages.
// pkg/resource.Registry satisfies this interface.
type ResourceRegistry interface {
	RegisterStatic(res types.StaticResource)
	RegisterTemplate(res types.TemplateResource)
}

// Extension is the interface that all datasource extensions must implement.
type Extension interface {
	// Name returns the extension identifier (e.g. "clickhouse").
	Name() string

	// Init parses the raw YAML config section for this extension.
	Init(rawConfig []byte) error

	// ApplyDefaults sets default values before validation.
	ApplyDefaults()

	// Validate checks that the parsed config is valid.
	Validate() error

	// SandboxEnv returns credential-free environment variables for the sandbox.
	// Credentials are never passed to sandbox containers - they connect via
	// the credential proxy instead.
	SandboxEnv() (map[string]string, error)

	// DatasourceInfo returns metadata about configured datasources
	// for the datasources:// MCP resource.
	DatasourceInfo() []types.DatasourceInfo

	// Examples returns query examples organized by category.
	Examples() map[string]types.ExampleCategory

	// PythonAPIDocs returns API documentation for the extension's
	// Python module, keyed by module name.
	PythonAPIDocs() map[string]types.ModuleDoc

	// GettingStartedSnippet returns a Markdown snippet to include
	// in the getting-started resource.
	GettingStartedSnippet() string

	// RegisterResources registers any custom MCP resources
	// (e.g. clickhouse://tables) with the resource registry.
	RegisterResources(log logrus.FieldLogger, reg ResourceRegistry) error

	// Start performs async initialization (e.g. schema discovery).
	Start(ctx context.Context) error

	// Stop cleans up resources.
	Stop(ctx context.Context) error
}
