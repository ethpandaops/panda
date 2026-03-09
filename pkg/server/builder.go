package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/mcp/pkg/app"
	"github.com/ethpandaops/mcp/pkg/auth"
	"github.com/ethpandaops/mcp/pkg/cartographoor"
	"github.com/ethpandaops/mcp/pkg/config"
	"github.com/ethpandaops/mcp/pkg/execsvc"
	"github.com/ethpandaops/mcp/pkg/extension"
	"github.com/ethpandaops/mcp/pkg/proxy"
	"github.com/ethpandaops/mcp/pkg/resource"
	"github.com/ethpandaops/mcp/pkg/sandbox"
	"github.com/ethpandaops/mcp/pkg/searchruntime"
	"github.com/ethpandaops/mcp/pkg/searchsvc"
	"github.com/ethpandaops/mcp/pkg/tool"
	"github.com/ethpandaops/mcp/runbooks"
)

// Dependencies contains all the services required to run the MCP server.
type Dependencies struct {
	Logger           logrus.FieldLogger
	Config           *config.Config
	ToolRegistry     tool.Registry
	ResourceRegistry resource.Registry
	Sandbox          sandbox.Service
	Auth             auth.SimpleService
}

// Builder constructs and wires all dependencies for the MCP server.
type Builder struct {
	log logrus.FieldLogger
	cfg *config.Config
}

// NewBuilder creates a new server builder.
func NewBuilder(log logrus.FieldLogger, cfg *config.Config) *Builder {
	return &Builder{
		log: log.WithField("component", "builder"),
		cfg: cfg,
	}
}

// Build constructs all dependencies and returns the server service.
func (b *Builder) Build(ctx context.Context) (Service, error) {
	b.log.Info("Building MCP server dependencies")

	// Build shared application components (extensions, sandbox, proxy, search indices).
	application := app.New(b.log, b.cfg)
	if err := application.Build(ctx); err != nil {
		return nil, err
	}

	searchRuntime, err := searchruntime.Build(b.log, b.cfg.SemanticSearch, application.ExtensionRegistry)
	if err != nil {
		_ = application.Stop(ctx)
		return nil, fmt.Errorf("building search runtime: %w", err)
	}

	// Create auth service (MCP-server-specific).
	authSvc, err := b.buildAuth()
	if err != nil {
		_ = application.Stop(ctx)
		_ = searchRuntime.Close()

		return nil, fmt.Errorf("building auth: %w", err)
	}

	if err := authSvc.Start(ctx); err != nil {
		_ = application.Stop(ctx)
		_ = searchRuntime.Close()

		return nil, fmt.Errorf("starting auth: %w", err)
	}

	if authSvc.Enabled() {
		b.log.Info("Auth service started")
	}

	// Create tool registry and register tools (MCP-server-specific).
	toolReg := b.buildToolRegistry(
		application.Sandbox,
		searchRuntime.ExampleIndex,
		application.ExtensionRegistry,
		application.ProxyClient,
		searchRuntime.RunbookRegistry,
		searchRuntime.RunbookIndex,
	)

	// Create resource registry and register resources (MCP-server-specific).
	resourceReg := b.buildResourceRegistry(
		application.Cartographoor,
		application.ExtensionRegistry,
		toolReg,
		application.ProxyClient,
	)

	searchSvc := searchsvc.New(
		searchRuntime.ExampleIndex,
		application.ExtensionRegistry,
		searchRuntime.RunbookIndex,
		searchRuntime.RunbookRegistry,
	)

	execSvc := execsvc.New(
		b.log,
		application.Sandbox,
		b.cfg,
		application.ExtensionRegistry,
		application.ProxyClient,
	)

	cleanup := func(stopCtx context.Context) error {
		var errs []error

		if err := searchRuntime.Close(); err != nil {
			errs = append(errs, err)
		}

		if err := application.Stop(stopCtx); err != nil {
			errs = append(errs, err)
		}

		return errors.Join(errs...)
	}

	// Create and return the server service.
	return NewService(
		b.log,
		b.cfg.Server,
		toolReg,
		resourceReg,
		authSvc,
		searchSvc,
		execSvc,
		application.ProxyClient,
		application.ExtensionRegistry,
		cleanup,
	), nil
}

// buildAuth creates the auth service.
func (b *Builder) buildAuth() (auth.SimpleService, error) {
	return auth.NewSimpleService(b.log, b.cfg.Auth, b.cfg.Server.BaseURL)
}

// buildToolRegistry creates and populates the tool registry.
func (b *Builder) buildToolRegistry(
	sandboxSvc sandbox.Service,
	exampleIndex *resource.ExampleIndex,
	extensionReg *extension.Registry,
	proxyClient proxy.Service,
	runbookReg *runbooks.Registry,
	runbookIndex *resource.RunbookIndex,
) tool.Registry {
	reg := tool.NewRegistry(b.log)

	// Register execute_python tool.
	reg.Register(tool.NewExecutePythonTool(b.log, sandboxSvc, b.cfg, extensionReg, proxyClient))

	// Register manage_session tool.
	reg.Register(tool.NewManageSessionTool(b.log, sandboxSvc, b.cfg, extensionReg, proxyClient))

	// Register unified search tool when either search index is available.
	if exampleIndex != nil || (runbookIndex != nil && runbookReg != nil) {
		reg.Register(tool.NewSearchTool(b.log, exampleIndex, extensionReg, runbookIndex, runbookReg))
	}

	b.log.WithField("tool_count", len(reg.List())).Info("Tool registry built")

	return reg
}

// buildResourceRegistry creates and populates the resource registry.
func (b *Builder) buildResourceRegistry(
	cartographoorClient cartographoor.CartographoorClient,
	extensionReg *extension.Registry,
	toolReg tool.Registry,
	proxyClient proxy.Service,
) resource.Registry {
	reg := resource.NewRegistry(b.log)

	// Register datasources resources (from extension registry and proxy client).
	resource.RegisterDatasourcesResources(b.log, reg, extensionReg, proxyClient)

	// Register examples resources (from extension registry).
	resource.RegisterExamplesResources(b.log, reg, extensionReg)

	// Register networks resources.
	resource.RegisterNetworksResources(b.log, reg, cartographoorClient)

	// Register Python library API resources (from extension registry).
	resource.RegisterAPIResources(b.log, reg, extensionReg)

	// Register getting-started resource.
	resource.RegisterGettingStartedResources(b.log, reg, toolReg, extensionReg)

	// Register extension-specific resources (e.g., clickhouse://tables).
	for _, ext := range extensionReg.Initialized() {
		if err := ext.RegisterResources(b.log, reg); err != nil {
			b.log.WithError(err).WithField("extension", ext.Name()).Warn("Failed to register extension resources")
		}
	}

	staticCount := len(reg.ListStatic())
	templateCount := len(reg.ListTemplates())

	b.log.WithFields(logrus.Fields{
		"static_count":   staticCount,
		"template_count": templateCount,
	}).Info("Resource registry built")

	return reg
}
