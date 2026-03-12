package proxyserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sirupsen/logrus"

	simpleauth "github.com/ethpandaops/panda/pkg/auth"
	proxyapi "github.com/ethpandaops/panda/pkg/proxy"
	"github.com/ethpandaops/panda/pkg/proxy/transport"
	"github.com/ethpandaops/panda/pkg/serverapi"
	"github.com/ethpandaops/panda/pkg/types"
)

// Server is the credential proxy server interface.
// This is the standalone proxy server that runs separately from the MCP server.
type Server interface {
	// Start starts the proxy server.
	Start(ctx context.Context) error

	// Stop stops the proxy server.
	Stop(ctx context.Context) error

	// URL returns the proxy URL.
	URL() string

	// ClickHouseDatasources returns the list of ClickHouse datasource names.
	ClickHouseDatasources() []string

	// PrometheusDatasources returns the list of Prometheus datasource names.
	PrometheusDatasources() []string

	// LokiDatasources returns the list of Loki datasource names.
	LokiDatasources() []string

	// S3Bucket returns the configured S3 bucket name.
	S3Bucket() string
}

// server implements the Server interface.
type server struct {
	log     logrus.FieldLogger
	cfg     ServerConfig
	httpSrv *http.Server
	mux     *chi.Mux
	url     string

	authenticator Authenticator
	authService   simpleauth.SimpleService
	rateLimiter   *RateLimiter
	auditor       *Auditor

	clickhouseHandler *transport.ClickHouseHandler
	prometheusHandler *transport.PrometheusHandler
	lokiHandler       *transport.LokiHandler
	s3Handler         *transport.S3Handler
	ethNodeHandler    *transport.EthNodeHandler

	mu      sync.RWMutex
	started bool
}

// Compile-time interface check.
var (
	_ Server = (*server)(nil)
)

// NewServer creates a new proxy server.
func NewServer(log logrus.FieldLogger, cfg ServerConfig) (Server, error) {
	hostURL, port := advertisedURLs(cfg.Server.ListenAddr)

	return newServer(log, cfg, hostURL, port)
}

func newServer(log logrus.FieldLogger, cfg ServerConfig, hostURL, port string) (*server, error) {
	s := &server{
		log: log.WithField("component", "proxy"),
		cfg: cfg,
		mux: chi.NewRouter(),
		url: hostURL,
	}

	// Create authenticator based on mode.
	switch cfg.Auth.Mode {
	case AuthModeNone:
		s.authenticator = NewNoneAuthenticator(log)
	case AuthModeOAuth:
		authCfg := simpleauth.Config{
			Enabled:     true,
			GitHub:      cfg.Auth.GitHub,
			AllowedOrgs: append([]string(nil), cfg.Auth.AllowedOrgs...),
			Tokens:      cfg.Auth.Tokens,
			SuccessPage: cfg.Auth.SuccessPage,
		}

		authSvc, err := simpleauth.NewSimpleService(log, authCfg)
		if err != nil {
			return nil, fmt.Errorf("creating proxy auth service: %w", err)
		}

		s.authService = authSvc
		s.authenticator = NewSimpleServiceAuthenticator(authSvc)
	default:
		return nil, fmt.Errorf("unsupported auth mode: %s", cfg.Auth.Mode)
	}

	// Create rate limiter if enabled.
	if cfg.RateLimiting.Enabled {
		s.rateLimiter = NewRateLimiter(log, RateLimiterConfig{
			RequestsPerMinute: cfg.RateLimiting.RequestsPerMinute,
			BurstSize:         cfg.RateLimiting.BurstSize,
		})
	}

	// Create auditor if enabled.
	if cfg.Audit.Enabled {
		s.auditor = NewAuditor(log, AuditorConfig{
			LogQueries:     cfg.Audit.LogQueries,
			MaxQueryLength: cfg.Audit.MaxQueryLength,
		})
	}

	// Create handlers from config.
	s.clickhouseHandler = newClickHouseHandler(log, cfg.ClickHouse)
	s.prometheusHandler = newPrometheusHandler(log, cfg.Prometheus)
	s3Handler := newS3Handler(log, cfg.S3)
	ethNodeHandler := newEthNodeHandler(log, cfg.EthNode)

	lokiHandler, err := newLokiHandler(log, cfg.Loki)
	if err != nil {
		return nil, fmt.Errorf("creating loki handler: %w", err)
	}

	s.lokiHandler = lokiHandler
	s.s3Handler = s3Handler
	s.ethNodeHandler = ethNodeHandler

	if s.url == "" {
		s.url = fmt.Sprintf("http://localhost:%s", port)
	}

	// Register routes.
	s.registerRoutes()

	return s, nil
}

// registerRoutes sets up the HTTP routes.
func (s *server) registerRoutes() {
	// Health check endpoint (no auth required).
	s.mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Ready check endpoint (no auth required).
	s.mux.HandleFunc("/ready", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.RLock()
		ready := s.started
		s.mu.RUnlock()

		if ready {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ready"))
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("not ready"))
		}
	})

	// Datasources info endpoint (for discovery by MCP server and Python modules).
	// Build middleware chain.
	chain := s.buildMiddlewareChain()

	if s.authService != nil {
		s.authService.MountRoutes(s.mux)
	}

	s.mux.Handle("/datasources", chain(http.HandlerFunc(s.handleDatasources)))

	// Authenticated routes.
	if s.clickhouseHandler != nil {
		s.handleSubtreeRoute("/clickhouse", chain(s.clickhouseHandler))
	}

	if s.prometheusHandler != nil {
		s.handleSubtreeRoute("/prometheus", chain(s.prometheusHandler))
	}

	if s.lokiHandler != nil {
		s.handleSubtreeRoute("/loki", chain(s.lokiHandler))
	}

	if s.s3Handler != nil {
		s.handleSubtreeRoute("/s3", chain(s.s3Handler))
	}

	if s.ethNodeHandler != nil {
		s.handleSubtreeRoute("/beacon", chain(s.ethNodeHandler))
		s.handleSubtreeRoute("/execution", chain(s.ethNodeHandler))
	}
}

func (s *server) handleSubtreeRoute(pattern string, handler http.Handler) {
	base := strings.TrimSuffix(pattern, "/")

	s.mux.Handle(base, handler)
	s.mux.Handle(base+"/", handler)
	s.mux.Handle(base+"/*", handler)
}

// buildMiddlewareChain builds the middleware chain for authenticated routes.
func (s *server) buildMiddlewareChain() func(http.Handler) http.Handler {
	return func(handler http.Handler) http.Handler {
		h := handler

		// Audit logging (innermost).
		if s.auditor != nil {
			h = s.auditor.Middleware()(h)
		}

		// Rate limiting.
		if s.rateLimiter != nil {
			h = s.rateLimiter.Middleware()(h)
		}

		// Per-user request metrics (after auth, wraps rate limiting so 429s are counted).
		h = metricsMiddleware(h)

		// Authentication (outermost).
		h = s.authenticator.Middleware()(h)

		return h
	}
}

// handleDatasources returns the list of available datasources.
func (s *server) handleDatasources(w http.ResponseWriter, _ *http.Request) {
	info := s.Datasources()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(info); err != nil {
		s.log.WithError(err).Error("Failed to encode datasources response")
	}
}

// Start starts the proxy server.
func (s *server) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.started {
		return fmt.Errorf("proxy already started")
	}

	// Start authenticator.
	if err := s.authenticator.Start(ctx); err != nil {
		return fmt.Errorf("starting authenticator: %w", err)
	}

	// Create listener first to detect port conflicts immediately.
	listener, err := net.Listen("tcp", s.cfg.Server.ListenAddr)
	if err != nil {
		return fmt.Errorf("binding to %s: %w", s.cfg.Server.ListenAddr, err)
	}

	s.httpSrv = &http.Server{
		Handler:           s.mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       s.cfg.Server.ReadTimeout,
		WriteTimeout:      s.cfg.Server.WriteTimeout,
		IdleTimeout:       s.cfg.Server.IdleTimeout,
		BaseContext:       func(_ net.Listener) context.Context { return ctx },
	}

	s.log.WithField("addr", s.cfg.Server.ListenAddr).Info("Starting proxy server")

	// Start server in background with the already-bound listener.
	go func() {
		if err := s.httpSrv.Serve(listener); err != nil && err != http.ErrServerClosed {
			s.log.WithError(err).Error("Proxy server error")
		}
	}()

	s.started = true

	return nil
}

// Stop stops the proxy server.
func (s *server) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.started {
		return nil
	}

	// Stop authenticator.
	if err := s.authenticator.Stop(); err != nil {
		s.log.WithError(err).Warn("Error stopping authenticator")
	}

	// Stop rate limiter.
	if s.rateLimiter != nil {
		s.rateLimiter.Stop()
	}

	// Shutdown HTTP server.
	if s.httpSrv != nil {
		if err := s.httpSrv.Shutdown(ctx); err != nil {
			return fmt.Errorf("shutting down proxy server: %w", err)
		}
	}

	s.started = false
	s.log.Info("Proxy server stopped")

	return nil
}

// URL returns the proxy URL.
func (s *server) URL() string {
	return s.url
}

func (s *server) AuthorizeRequest(_ *http.Request) error {
	return nil
}

func (s *server) RegisterToken(executionID string) string {
	return "none"
}

func (s *server) RevokeToken(executionID string) {
}

func (s *server) Datasources() serverapi.DatasourcesResponse {
	return serverapi.DatasourcesResponse{
		Datasources:       s.DatasourceInfo(),
		S3Bucket:          s.S3Bucket(),
		S3PublicURLPrefix: s.S3PublicURLPrefix(),
		EthNodeAvailable:  s.EthNodeAvailable(),
	}
}

// ClickHouseDatasources returns the list of ClickHouse datasource names.
func (s *server) ClickHouseDatasources() []string {
	if s.clickhouseHandler == nil {
		return nil
	}

	return s.clickhouseHandler.Datasources()
}

// ClickHouseDatasourceInfo returns detailed ClickHouse datasource info.
func (s *server) ClickHouseDatasourceInfo() []types.DatasourceInfo {
	if len(s.cfg.ClickHouse) == 0 {
		return nil
	}

	result := make([]types.DatasourceInfo, 0, len(s.cfg.ClickHouse))
	for _, ch := range s.cfg.ClickHouse {
		info := types.DatasourceInfo{
			Type:        "clickhouse",
			Name:        ch.Name,
			Description: ch.Description,
		}
		if ch.Database != "" {
			info.Metadata = map[string]string{
				"database": ch.Database,
			}
		}
		result = append(result, info)
	}

	return result
}

// PrometheusDatasources returns the list of Prometheus datasource names.
func (s *server) PrometheusDatasources() []string {
	if s.prometheusHandler == nil {
		return nil
	}

	return s.prometheusHandler.Instances()
}

// PrometheusDatasourceInfo returns detailed Prometheus datasource info.
func (s *server) PrometheusDatasourceInfo() []types.DatasourceInfo {
	if len(s.cfg.Prometheus) == 0 {
		return nil
	}

	result := make([]types.DatasourceInfo, 0, len(s.cfg.Prometheus))
	for _, prom := range s.cfg.Prometheus {
		info := types.DatasourceInfo{
			Type:        "prometheus",
			Name:        prom.Name,
			Description: prom.Description,
		}
		if prom.URL != "" {
			info.Metadata = map[string]string{
				"url": prom.URL,
			}
		}
		result = append(result, info)
	}

	return result
}

// LokiDatasources returns the list of Loki datasource names.
func (s *server) LokiDatasources() []string {
	if s.lokiHandler == nil {
		return nil
	}

	return s.lokiHandler.Instances()
}

// LokiDatasourceInfo returns detailed Loki datasource info.
func (s *server) LokiDatasourceInfo() []types.DatasourceInfo {
	if len(s.cfg.Loki) == 0 {
		return nil
	}

	result := make([]types.DatasourceInfo, 0, len(s.cfg.Loki))
	for _, loki := range s.cfg.Loki {
		info := types.DatasourceInfo{
			Type:        "loki",
			Name:        loki.Name,
			Description: loki.Description,
		}
		if loki.URL != "" {
			info.Metadata = map[string]string{
				"url": loki.URL,
			}
		}
		result = append(result, info)
	}

	return result
}

// S3Bucket returns the configured S3 bucket name.
func (s *server) S3Bucket() string {
	if s.s3Handler == nil {
		return ""
	}

	return s.s3Handler.Bucket()
}

// S3PublicURLPrefix returns the public URL prefix for S3 objects.
func (s *server) S3PublicURLPrefix() string {
	if s.s3Handler == nil {
		return ""
	}

	return s.s3Handler.PublicURLPrefix()
}

// EthNodeAvailable returns true if the ethnode handler is configured.
func (s *server) EthNodeAvailable() bool {
	return s.ethNodeHandler != nil
}

// DatasourceInfo returns all configured datasource metadata.
func (s *server) DatasourceInfo() []types.DatasourceInfo {
	infos := append([]types.DatasourceInfo{}, s.ClickHouseDatasourceInfo()...)
	infos = append(infos, s.PrometheusDatasourceInfo()...)
	infos = append(infos, s.LokiDatasourceInfo()...)

	if s.EthNodeAvailable() {
		infos = append(infos, types.DatasourceInfo{
			Type: "ethnode",
			Name: "ethnode",
		})
	}

	return proxyapi.CloneDatasourceInfo(infos)
}

func advertisedURLs(listenAddr string) (string, string) {
	port := "18081"
	if _, p, err := net.SplitHostPort(listenAddr); err == nil && p != "" {
		port = p
	}

	url := fmt.Sprintf("http://localhost:%s", port)

	return url, port
}
