// Package module defines the base lifecycle contract for built-in
// integrations plus optional capability interfaces for docs, resources,
// sandbox env, and datasource metadata.
package module

import (
	"context"
	"errors"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/mcp/pkg/cartographoor"
	"github.com/ethpandaops/mcp/pkg/proxy"
	"github.com/ethpandaops/mcp/pkg/types"
)

// ErrNoValidConfig indicates that a module was configured but has no valid
// entries (e.g., all clusters/instances have empty required fields).
// This is not an error - the module should be skipped gracefully.
var ErrNoValidConfig = errors.New("no valid configuration entries")

// CartographoorAware is an optional interface for modules that need
// network discovery data.
type CartographoorAware interface {
	SetCartographoorClient(client cartographoor.CartographoorClient)
}

// ProxyAware is an optional interface for modules that need raw proxy access.
type ProxyAware interface {
	SetProxyClient(client proxy.Service)
}

// DefaultEnabled is an optional interface that modules can implement
// to indicate they should be initialized even without explicit config.
// This is useful for modules like dora that work with discovered data
// and require no user configuration.
type DefaultEnabled interface {
	// DefaultEnabled returns true if the module should be initialized
	// without explicit config in the config file.
	DefaultEnabled() bool
}

// EnabledAware is an optional interface for modules that can be
// initialized but still disabled via config.
type EnabledAware interface {
	Enabled() bool
}

// ResourceRegistry is the interface modules use to register MCP resources.
// This avoids a circular dependency between module and resource packages.
// pkg/resource.Registry satisfies this interface.
type ResourceRegistry interface {
	RegisterStatic(res types.StaticResource)
	RegisterTemplate(res types.TemplateResource)
}

// SandboxEnvProvider contributes credential-free sandbox environment values.
type SandboxEnvProvider interface {
	SandboxEnv() (map[string]string, error)
}

// DatasourceInfoProvider contributes datasource metadata for datasources:// resources.
type DatasourceInfoProvider interface {
	DatasourceInfo() []types.DatasourceInfo
}

// ExamplesProvider contributes search examples and examples:// resources.
type ExamplesProvider interface {
	Examples() map[string]types.ExampleCategory
}

// PythonAPIDocsProvider contributes Python module docs.
type PythonAPIDocsProvider interface {
	PythonAPIDocs() map[string]types.ModuleDoc
}

// GettingStartedSnippetProvider contributes snippets to the getting-started resource.
type GettingStartedSnippetProvider interface {
	GettingStartedSnippet() string
}

// ResourceProvider contributes custom MCP resources.
type ResourceProvider interface {
	RegisterResources(log logrus.FieldLogger, reg ResourceRegistry) error
}

// Module is the minimal lifecycle/config contract for built-in integrations.
// Optional capabilities are expressed through the provider interfaces above.
type Module interface {
	// Name returns the module identifier (e.g. "clickhouse").
	Name() string

	// Init parses the raw YAML config section for this module.
	Init(rawConfig []byte) error

	// ApplyDefaults sets default values before validation.
	ApplyDefaults()

	// Validate checks that the parsed config is valid.
	Validate() error

	// Start performs async initialization (e.g. schema discovery).
	Start(ctx context.Context) error

	// Stop cleans up resources.
	Stop(ctx context.Context) error
}
