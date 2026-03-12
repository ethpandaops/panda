// Package module defines the base lifecycle contract for built-in
// integrations plus optional capability interfaces for docs, resources,
// sandbox env, and datasource metadata.
package module

import (
	"context"
	"errors"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/cartographoor"
	"github.com/ethpandaops/panda/pkg/proxy"
	"github.com/ethpandaops/panda/pkg/types"
)

// ErrNoValidConfig indicates that a module was configured but has no valid
// entries (e.g., all clusters/instances have empty required fields).
// This is not an error - the module should be skipped gracefully.
var ErrNoValidConfig = errors.New("no valid configuration entries")

// RuntimeDependencies are the shared runtime collaborators made available
// after bootstrap.
type RuntimeDependencies struct {
	ProxySchemaAccess proxy.ClickHouseSchemaAccess
	Cartographoor     cartographoor.CartographoorClient
}

// RuntimeDependencyBinder is for modules that need shared runtime collaborators.
type RuntimeDependencyBinder interface {
	BindRuntimeDependencies(deps RuntimeDependencies)
}

type Starter interface {
	Start(ctx context.Context) error
}

type Stopper interface {
	Stop(ctx context.Context) error
}

type DefaultsApplier interface {
	ApplyDefaults()
}

// ProxyDiscoverable modules initialize from datasources discovered via the proxy.
type ProxyDiscoverable interface {
	InitFromDiscovery(datasources []types.DatasourceInfo) error
}

// DefaultEnabled marks modules that should be initialized even without explicit config.
type DefaultEnabled interface {
	DefaultEnabled() bool
}

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

type SandboxEnvProvider interface {
	SandboxEnv() (map[string]string, error)
}

type ExamplesProvider interface {
	Examples() map[string]types.ExampleCategory
}

type PythonAPIDocsProvider interface {
	PythonAPIDocs() map[string]types.ModuleDoc
}

type GettingStartedSnippetProvider interface {
	GettingStartedSnippet() string
}

type ResourceProvider interface {
	RegisterResources(log logrus.FieldLogger, reg ResourceRegistry) error
}

// Module is the minimal config contract for built-in integrations.
// Optional lifecycle and capabilities are expressed through the provider
// interfaces above.
type Module interface {
	Name() string
	Init(rawConfig []byte) error
	Validate() error
}
