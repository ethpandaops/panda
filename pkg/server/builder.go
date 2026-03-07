package server

import (
	"context"
	"fmt"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/mcp/pkg/app"
	"github.com/ethpandaops/mcp/pkg/auth"
	"github.com/ethpandaops/mcp/pkg/config"
	"github.com/ethpandaops/mcp/pkg/eips"
	"github.com/ethpandaops/mcp/pkg/plugin"
	"github.com/ethpandaops/mcp/pkg/proxy"
	"github.com/ethpandaops/mcp/pkg/resource"
	"github.com/ethpandaops/mcp/pkg/sandbox"
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

	// Build shared application components (plugins, sandbox, proxy, search indices).
	application := app.New(b.log, b.cfg)
	if err := application.Build(ctx); err != nil {
		return nil, err
	}

	// Create auth service (MCP-server-specific).
	authSvc, err := b.buildAuth()
	if err != nil {
		_ = application.Stop(ctx)

		return nil, fmt.Errorf("building auth: %w", err)
	}

	if err := authSvc.Start(ctx); err != nil {
		_ = application.Stop(ctx)

		return nil, fmt.Errorf("starting auth: %w", err)
	}

	if authSvc.Enabled() {
		b.log.Info("Auth service started")
	}

	// Create tool registry and register tools (MCP-server-specific).
	toolReg := b.buildToolRegistry(
		application.Sandbox,
		application.ExampleIndex,
		application.PluginRegistry,
		application.ProxyClient,
		application.RunbookRegistry,
		application.RunbookIndex,
		application.EIPRegistry,
		application.EIPIndex,
	)

	// Create resource registry and register resources (MCP-server-specific).
	resourceReg := b.buildResourceRegistry(
		application.Cartographoor,
		application.PluginRegistry,
		toolReg,
		application.ProxyClient,
	)

	// Create and return the server service.
	return NewService(
		b.log,
		b.cfg.Server,
		b.cfg.Auth,
		toolReg,
		resourceReg,
		application.Sandbox,
		authSvc,
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
	pluginReg *plugin.Registry,
	proxyClient proxy.Service,
	runbookReg *runbooks.Registry,
	runbookIndex *resource.RunbookIndex,
	eipReg *eips.Registry,
	eipIndex *resource.EIPIndex,
) tool.Registry {
	reg := tool.NewRegistry(b.log)

	// Register execute_python tool.
	reg.Register(tool.NewExecutePythonTool(b.log, sandboxSvc, b.cfg, pluginReg, proxyClient))

	// Register manage_session tool.
	reg.Register(tool.NewManageSessionTool(b.log, sandboxSvc))

	// Register search_examples tool (requires example index).
	if exampleIndex != nil {
		reg.Register(tool.NewSearchExamplesTool(b.log, exampleIndex, pluginReg))
	}

	// Register search_runbooks tool (requires runbook index).
	if runbookIndex != nil && runbookReg != nil {
		reg.Register(tool.NewSearchRunbooksTool(b.log, runbookIndex, runbookReg))
	}

	// Register search_eips tool (requires EIP index).
	if eipIndex != nil && eipReg != nil {
		reg.Register(tool.NewSearchEIPsTool(b.log, eipIndex, eipReg))
	}

	b.log.WithField("tool_count", len(reg.List())).Info("Tool registry built")

	return reg
}

// buildResourceRegistry creates and populates the resource registry.
func (b *Builder) buildResourceRegistry(
	cartographoorClient resource.CartographoorClient,
	pluginReg *plugin.Registry,
	toolReg tool.Registry,
	proxyClient proxy.Client,
) resource.Registry {
	reg := resource.NewRegistry(b.log)

	// Register datasources resources (from plugin registry and proxy client).
	resource.RegisterDatasourcesResources(b.log, reg, pluginReg, proxyClient)

	// Register examples resources (from plugin registry).
	resource.RegisterExamplesResources(b.log, reg, pluginReg)

	// Register networks resources.
	resource.RegisterNetworksResources(b.log, reg, cartographoorClient)

	// Register Python library API resources (from plugin registry).
	resource.RegisterAPIResources(b.log, reg, pluginReg)

	// Register getting-started resource.
	resource.RegisterGettingStartedResources(b.log, reg, toolReg, pluginReg)

	// Register plugin-specific resources (e.g., clickhouse://tables).
	for _, p := range pluginReg.Initialized() {
		if err := p.RegisterResources(b.log, reg); err != nil {
			b.log.WithError(err).WithField("plugin", p.Name()).Warn("Failed to register plugin resources")
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
