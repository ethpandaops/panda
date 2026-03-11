// Package server provides the MCP server implementation for ethpandaops-panda.
package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/internal/version"
	"github.com/ethpandaops/panda/pkg/cartographoor"
	"github.com/ethpandaops/panda/pkg/config"
	"github.com/ethpandaops/panda/pkg/execsvc"
	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/observability"
	"github.com/ethpandaops/panda/pkg/proxy"
	"github.com/ethpandaops/panda/pkg/resource"
	"github.com/ethpandaops/panda/pkg/searchsvc"
	"github.com/ethpandaops/panda/pkg/serverapi"
	"github.com/ethpandaops/panda/pkg/storage"
	"github.com/ethpandaops/panda/pkg/tokenstore"
	"github.com/ethpandaops/panda/pkg/tool"
	"github.com/ethpandaops/panda/pkg/types"
)

// Service is the main MCP server service.
type Service interface {
	// Start initializes and starts the MCP server.
	Start(ctx context.Context) error
	// Stop gracefully shuts down the server.
	Stop() error
}

// service implements the Service interface.
type service struct {
	log                  logrus.FieldLogger
	cfg                  config.ServerConfig
	toolRegistry         tool.Registry
	resourceRegistry     resource.Registry
	searchService        *searchsvc.Service
	execService          *execsvc.Service
	proxyService         proxy.Service
	storageService       storage.Service
	moduleRegistry       *module.Registry
	cartographoorClient  cartographoor.CartographoorClient
	proxyAuthMetadata    *serverapi.ProxyAuthMetadataResponse
	runtimeTokens        *tokenstore.Store
	cleanup              func(context.Context) error
	httpClient           *http.Client
	operationHandlers    map[string]operationHandler
	mcpServer            *mcpserver.MCPServer
	sseServer            *mcpserver.SSEServer
	streamableHTTPServer *mcpserver.StreamableHTTPServer
	httpServer           *http.Server
	mu                   sync.Mutex
	done                 chan struct{}
	running              bool
}

// NewService creates a new MCP server service.
func NewService(
	log logrus.FieldLogger,
	cfg config.ServerConfig,
	toolRegistry tool.Registry,
	resourceRegistry resource.Registry,
	searchSvc *searchsvc.Service,
	execSvc *execsvc.Service,
	proxySvc proxy.Service,
	storageSvc storage.Service,
	moduleReg *module.Registry,
	cartographoorClient cartographoor.CartographoorClient,
	proxyAuthMetadata *serverapi.ProxyAuthMetadataResponse,
	runtimeTokens *tokenstore.Store,
	cleanup func(context.Context) error,
) Service {
	srv := &service{
		log:                 log.WithField("component", "server"),
		cfg:                 cfg,
		toolRegistry:        toolRegistry,
		resourceRegistry:    resourceRegistry,
		searchService:       searchSvc,
		execService:         execSvc,
		proxyService:        proxySvc,
		storageService:      storageSvc,
		moduleRegistry:      moduleReg,
		cartographoorClient: cartographoorClient,
		proxyAuthMetadata:   proxyAuthMetadata,
		runtimeTokens:       runtimeTokens,
		cleanup:             cleanup,
		httpClient:          &http.Client{Transport: &version.Transport{}, Timeout: 0},
		operationHandlers:   make(map[string]operationHandler, 32),
		done:                make(chan struct{}),
	}

	srv.registerOperations()

	return srv
}

// Start initializes and starts the MCP server.
func (s *service) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()

		return errors.New("server already running")
	}

	s.running = true
	s.mu.Unlock()

	s.log.WithField("version", version.Version).Info("Starting MCP server")

	// Create the MCP server
	s.mcpServer = mcpserver.NewMCPServer(
		"ethpandaops-panda",
		version.Version,
		mcpserver.WithToolCapabilities(true),
		mcpserver.WithResourceCapabilities(true, true),
		mcpserver.WithLogging(),
	)

	// Register tools
	s.registerTools()

	// Register resources
	s.registerResources()

	return s.runHTTP(ctx)
}

// Stop gracefully shuts down the server.
func (s *service) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return nil
	}

	s.log.Info("Stopping MCP server")

	close(s.done)
	s.running = false

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if s.httpServer != nil {
		if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
			s.log.WithError(err).Error("Failed to shutdown HTTP server")
		}
	}

	if s.sseServer != nil {
		if err := s.sseServer.Shutdown(shutdownCtx); err != nil {
			s.log.WithError(err).Error("Failed to shutdown SSE server")
		}
	}

	if s.streamableHTTPServer != nil {
		if err := s.streamableHTTPServer.Shutdown(shutdownCtx); err != nil {
			s.log.WithError(err).Error("Failed to shutdown Streamable HTTP server")
		}
	}

	if s.cleanup != nil {
		if err := s.cleanup(shutdownCtx); err != nil {
			s.log.WithError(err).Error("Failed to stop server dependencies")
		}
	}

	if s.runtimeTokens != nil {
		s.runtimeTokens.Stop()
	}

	s.log.Info("MCP server stopped")

	return nil
}

// registerTools registers all tools with the MCP server.
func (s *service) registerTools() {
	for _, def := range s.toolRegistry.Definitions() {
		s.log.WithField("tool", def.Tool.Name).Debug("Registering tool with MCP server")

		// Wrap the handler to add metrics
		handler := s.wrapToolHandler(def.Tool.Name, def.Handler)
		s.mcpServer.AddTool(def.Tool, handler)
	}
}

// registerResources registers all resources with the MCP server.
func (s *service) registerResources() {
	// Register static resources
	for _, res := range s.resourceRegistry.ListStatic() {
		s.log.WithField("uri", res.URI).Debug("Registering static resource with MCP server")

		uri := res.URI
		s.mcpServer.AddResource(res, s.createResourceHandler(uri))
	}

	// Register template resources
	for _, tmpl := range s.resourceRegistry.ListTemplates() {
		templateURI := ""
		if tmpl.URITemplate != nil {
			templateURI = tmpl.URITemplate.Raw()
		}

		s.log.WithField("template", templateURI).Debug("Registering template resource with MCP server")

		s.mcpServer.AddResourceTemplate(tmpl, s.createResourceTemplateHandler())
	}
}

// wrapToolHandler wraps a tool handler with metrics.
func (s *service) wrapToolHandler(toolName string, handler tool.Handler) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		startTime := time.Now()

		result, err := handler(ctx, req)

		// Record duration.
		duration := time.Since(startTime).Seconds()
		observability.ToolCallDuration.WithLabelValues(toolName).Observe(duration)

		if err != nil {
			observability.ToolCallsTotal.WithLabelValues(toolName, "error").Inc()

			return nil, err
		}

		observability.ToolCallsTotal.WithLabelValues(toolName, "success").Inc()

		return result, nil
	}
}

// createResourceHandler creates a resource handler for a static resource.
func (s *service) createResourceHandler(uri string) mcpserver.ResourceHandlerFunc {
	return func(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		ctx = types.WithClientContext(ctx, types.ClientContextMCP)
		content, mimeType, err := s.resourceRegistry.Read(ctx, uri)
		if err != nil {
			return nil, err
		}

		return []mcp.ResourceContents{
			mcp.TextResourceContents{
				URI:      uri,
				MIMEType: mimeType,
				Text:     content,
			},
		}, nil
	}
}

// createResourceTemplateHandler creates a handler for template resources.
func (s *service) createResourceTemplateHandler() mcpserver.ResourceTemplateHandlerFunc {
	return func(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		ctx = types.WithClientContext(ctx, types.ClientContextMCP)
		content, mimeType, err := s.resourceRegistry.Read(ctx, req.Params.URI)
		if err != nil {
			return nil, err
		}

		return []mcp.ResourceContents{
			mcp.TextResourceContents{
				URI:      req.Params.URI,
				MIMEType: mimeType,
				Text:     content,
			},
		}, nil
	}
}

// runHTTP runs the server with both SSE and streamable-http MCP transports.
func (s *service) runHTTP(ctx context.Context) error {
	addr := fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.Port)

	s.log.WithField("address", addr).Info("Running MCP server with HTTP transport (SSE + streamable-http)")

	// Create SSE server.
	sseOpts := []mcpserver.SSEOption{
		mcpserver.WithKeepAlive(true),
	}

	if s.cfg.BaseURL != "" {
		sseOpts = append(sseOpts, mcpserver.WithBaseURL(s.cfg.BaseURL))
	}

	s.sseServer = mcpserver.NewSSEServer(s.mcpServer, sseOpts...)

	// Create streamable-http server.
	s.streamableHTTPServer = mcpserver.NewStreamableHTTPServer(s.mcpServer)

	// Mount both transports on the same handler.
	handler := s.buildHTTPHandler(map[string]http.Handler{
		"/sse":       s.sseServer,
		"/sse/*":     s.sseServer,
		"/message":   s.sseServer,
		"/message/*": s.sseServer,
		"/mcp":       s.streamableHTTPServer,
		"/mcp/*":     s.streamableHTTPServer,
	})

	s.httpServer = &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)

	go func() {
		errCh <- s.httpServer.ListenAndServe()
	}()

	observability.ActiveConnections.Inc()
	defer observability.ActiveConnections.Dec()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		return s.httpServer.Shutdown(shutdownCtx)
	case <-s.done:
		return nil
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}

		return nil
	}
}

// buildHTTPHandler creates an HTTP handler with health, API, and MCP routes.
func (s *service) buildHTTPHandler(routes map[string]http.Handler) http.Handler {
	r := chi.NewRouter()

	// Health endpoints.
	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	r.Get("/ready", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})

	s.mountAPIRoutes(r)

	// Mount MCP handler at specified routes.
	for pattern, handler := range routes {
		r.Handle(pattern, handler)
	}

	return r
}

// Compile-time interface compliance check.
var _ Service = (*service)(nil)
