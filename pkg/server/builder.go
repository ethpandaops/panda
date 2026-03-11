package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/spf13/afero"

	"github.com/ethpandaops/panda/pkg/app"
	"github.com/ethpandaops/panda/pkg/cartographoor"
	"github.com/ethpandaops/panda/pkg/config"
	"github.com/ethpandaops/panda/pkg/execsvc"
	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/resource"
	"github.com/ethpandaops/panda/pkg/sandbox"
	"github.com/ethpandaops/panda/pkg/searchruntime"
	"github.com/ethpandaops/panda/pkg/searchsvc"
	"github.com/ethpandaops/panda/pkg/serverapi"
	"github.com/ethpandaops/panda/pkg/storage"
	"github.com/ethpandaops/panda/pkg/tokenstore"
	"github.com/ethpandaops/panda/pkg/tool"
	"github.com/ethpandaops/panda/runbooks"
)

// Dependencies contains all the services required to run the MCP server.
type Dependencies struct {
	Logger           logrus.FieldLogger
	Config           *config.Config
	ToolRegistry     tool.Registry
	ResourceRegistry resource.Registry
	Sandbox          sandbox.Service
	ProxyAuth        *serverapi.ProxyAuthMetadataResponse
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

	// Build shared application components (modules, sandbox, proxy, search indices).
	application := app.New(b.log, b.cfg)
	if err := application.Build(ctx); err != nil {
		return nil, err
	}

	searchRuntime, err := searchruntime.Build(b.log, b.cfg.SemanticSearch, application.ModuleRegistry)
	if err != nil {
		_ = application.Stop(ctx)
		return nil, fmt.Errorf("building search runtime: %w", err)
	}

	var (
		exampleIndex    *resource.ExampleIndex
		runbookRegistry *runbooks.Registry
		runbookIndex    *resource.RunbookIndex
	)

	if searchRuntime != nil {
		exampleIndex = searchRuntime.ExampleIndex
		runbookRegistry = searchRuntime.RunbookRegistry
		runbookIndex = searchRuntime.RunbookIndex
	}

	searchSvc := searchsvc.New(
		exampleIndex,
		application.ModuleRegistry,
		runbookIndex,
		runbookRegistry,
	)

	runtimeTokens := tokenstore.New(2 * time.Hour)

	execSvc := execsvc.New(
		b.log,
		application.Sandbox,
		b.cfg,
		application.ModuleRegistry,
		runtimeTokens,
	)

	// Create tool registry and register tools (MCP-server-specific).
	toolReg := b.buildToolRegistry(
		application.Sandbox,
		execSvc,
		exampleIndex,
		application.ModuleRegistry,
		runbookRegistry,
		runbookIndex,
	)

	// Create resource registry and register resources (MCP-server-specific).
	resourceReg := b.buildResourceRegistry(
		application.Cartographoor,
		application.ModuleRegistry,
		toolReg,
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

	// Resolve server base URL for storage URL construction.
	serverBaseURL := strings.TrimSpace(b.cfg.Server.BaseURL)
	if serverBaseURL == "" {
		serverBaseURL = fmt.Sprintf("http://localhost:%d", b.cfg.Server.Port)
	}

	// Create local file storage service.
	storageSvc := storage.New(
		afero.NewOsFs(),
		b.cfg.Storage.BaseDir,
		serverBaseURL,
	)

	// Create and return the server service.
	return NewService(
		b.log,
		b.cfg.Server,
		toolReg,
		resourceReg,
		searchSvc,
		execSvc,
		application.ProxyClient,
		storageSvc,
		application.ModuleRegistry,
		application.Cartographoor,
		buildProxyAuthMetadata(b.cfg),
		runtimeTokens,
		cleanup,
	), nil
}

// buildToolRegistry creates and populates the tool registry.
func (b *Builder) buildToolRegistry(
	sandboxSvc sandbox.Service,
	execSvc *execsvc.Service,
	exampleIndex *resource.ExampleIndex,
	moduleReg *module.Registry,
	runbookReg *runbooks.Registry,
	runbookIndex *resource.RunbookIndex,
) tool.Registry {
	reg := tool.NewRegistry(b.log)

	// Register execute_python tool.
	reg.Register(tool.NewExecutePythonTool(b.log, sandboxSvc, b.cfg, execSvc))

	// Register manage_session tool.
	reg.Register(tool.NewManageSessionTool(b.log, execSvc))

	// Register unified search tool when either search index is available.
	if exampleIndex != nil || (runbookIndex != nil && runbookReg != nil) {
		reg.Register(tool.NewSearchTool(b.log, exampleIndex, moduleReg, runbookIndex, runbookReg))
	}

	b.log.WithField("tool_count", len(reg.List())).Info("Tool registry built")

	return reg
}

func buildProxyAuthMetadata(cfg *config.Config) *serverapi.ProxyAuthMetadataResponse {
	if cfg == nil || cfg.Proxy.Auth == nil {
		return &serverapi.ProxyAuthMetadataResponse{}
	}

	issuerURL := strings.TrimSpace(cfg.Proxy.Auth.IssuerURL)
	if issuerURL == "" {
		issuerURL = strings.TrimRight(cfg.Proxy.URL, "/")
	}

	resource := strings.TrimRight(cfg.Proxy.URL, "/")

	return &serverapi.ProxyAuthMetadataResponse{
		Enabled:   issuerURL != "" && cfg.Proxy.Auth.ClientID != "",
		IssuerURL: issuerURL,
		ClientID:  cfg.Proxy.Auth.ClientID,
		Resource:  resource,
	}
}

// buildResourceRegistry creates and populates the resource registry.
func (b *Builder) buildResourceRegistry(
	cartographoorClient cartographoor.CartographoorClient,
	moduleReg *module.Registry,
	toolReg tool.Registry,
) resource.Registry {
	reg := resource.NewRegistry(b.log)

	// Register datasources resources (from module registry).
	resource.RegisterDatasourcesResources(b.log, reg, moduleReg)

	// Register examples resources (from module registry).
	resource.RegisterExamplesResources(b.log, reg, moduleReg)

	// Register networks resources.
	resource.RegisterNetworksResources(b.log, reg, cartographoorClient)

	// Register Python library API resources (from module registry).
	resource.RegisterAPIResources(b.log, reg, moduleReg)

	// Register getting-started resource.
	resource.RegisterGettingStartedResources(b.log, reg, toolReg, moduleReg)

	// Register module-specific resources (e.g., clickhouse://tables).
	for _, ext := range moduleReg.Initialized() {
		provider, ok := ext.(module.ResourceProvider)
		if !ok {
			continue
		}

		if err := provider.RegisterResources(b.log, reg); err != nil {
			b.log.WithError(err).WithField("module", ext.Name()).Warn("Failed to register module resources")
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
