package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/mcp/pkg/proxy/handlers"
	"github.com/ethpandaops/mcp/pkg/types"
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
	mux     *http.ServeMux

	authenticator Authenticator
	rateLimiter   *RateLimiter
	auditor       *Auditor

	clickhouseHandler *handlers.ClickHouseHandler
	clickhouseOps     *handlers.ClickHouseOperationsHandler
	prometheusHandler *handlers.PrometheusHandler
	lokiHandler       *handlers.LokiHandler
	s3Handler         *handlers.S3Handler
	ethNodeHandler    *handlers.EthNodeHandler

	mu      sync.RWMutex
	started bool
}

// Compile-time interface check.
var _ Server = (*server)(nil)

// NewServer creates a new proxy server.
func NewServer(log logrus.FieldLogger, cfg ServerConfig) (Server, error) {
	s := &server{
		log: log.WithField("component", "proxy"),
		cfg: cfg,
		mux: http.NewServeMux(),
	}

	// Create authenticator based on mode.
	switch cfg.Auth.Mode {
	case AuthModeNone:
		s.authenticator = NewNoneAuthenticator(log)
	case AuthModeToken:
		tokens := NewTokenStore(cfg.Auth.TokenTTL)
		s.authenticator = NewTokenAuthenticator(log, tokens)
	case AuthModeJWT:
		if cfg.Auth.JWT == nil {
			return nil, fmt.Errorf("JWT config is required for JWT auth mode")
		}

		validator := NewJWTValidator(log, *cfg.Auth.JWT)
		s.authenticator = NewJWTAuthenticator(log, validator)
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
	chConfigs, promConfigs, lokiConfigs, s3Config, ethNodeConfig := cfg.ToHandlerConfigs()

	if len(chConfigs) > 0 {
		s.clickhouseHandler = handlers.NewClickHouseHandler(log, chConfigs)
		s.clickhouseOps = handlers.NewClickHouseOperationsHandler(log, chConfigs)
	}

	if len(promConfigs) > 0 {
		s.prometheusHandler = handlers.NewPrometheusHandler(log, promConfigs)
	}

	if len(lokiConfigs) > 0 {
		s.lokiHandler = handlers.NewLokiHandler(log, lokiConfigs)
	}

	if s3Config != nil && s3Config.Endpoint != "" {
		s.s3Handler = handlers.NewS3Handler(log, s3Config)
	}

	if ethNodeConfig != nil {
		s.ethNodeHandler = handlers.NewEthNodeHandler(log, *ethNodeConfig)
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
	s.mux.HandleFunc("/datasources", s.handleDatasources)

	// Build middleware chain.
	chain := s.buildMiddlewareChain()

	// Authenticated routes.
	if s.clickhouseHandler != nil {
		s.mux.Handle("/clickhouse/", chain(s.clickhouseHandler))
	}

	if s.clickhouseOps != nil {
		s.mux.Handle("/api/v1/operations/", chain(s.clickhouseOps))
	}

	if s.prometheusHandler != nil {
		s.mux.Handle("/prometheus/", chain(s.prometheusHandler))
	}

	if s.lokiHandler != nil {
		s.mux.Handle("/loki/", chain(s.lokiHandler))
	}

	if s.s3Handler != nil {
		s.mux.Handle("/s3/", chain(s.s3Handler))
	}

	if s.ethNodeHandler != nil {
		s.mux.Handle("/beacon/", chain(s.ethNodeHandler))
		s.mux.Handle("/execution/", chain(s.ethNodeHandler))
	}
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

		// Authentication (outermost).
		h = s.authenticator.Middleware()(h)

		return h
	}
}

// DatasourcesResponse is the response from the /datasources endpoint.
// This is used by the MCP server client to discover available datasources.
type DatasourcesResponse struct {
	ClickHouse        []string               `json:"clickhouse,omitempty"`
	Prometheus        []string               `json:"prometheus,omitempty"`
	Loki              []string               `json:"loki,omitempty"`
	ClickHouseInfo    []types.DatasourceInfo `json:"clickhouse_info,omitempty"`
	PrometheusInfo    []types.DatasourceInfo `json:"prometheus_info,omitempty"`
	LokiInfo          []types.DatasourceInfo `json:"loki_info,omitempty"`
	S3Bucket          string                 `json:"s3_bucket,omitempty"`
	S3PublicURLPrefix string                 `json:"s3_public_url_prefix,omitempty"`
	EthNodeAvailable  bool                   `json:"ethnode_available,omitempty"`
}

// handleDatasources returns the list of available datasources.
func (s *server) handleDatasources(w http.ResponseWriter, _ *http.Request) {
	info := DatasourcesResponse{
		ClickHouse:        s.ClickHouseDatasources(),
		Prometheus:        s.PrometheusDatasources(),
		Loki:              s.LokiDatasources(),
		ClickHouseInfo:    s.ClickHouseDatasourceInfo(),
		PrometheusInfo:    s.PrometheusDatasourceInfo(),
		LokiInfo:          s.LokiDatasourceInfo(),
		S3Bucket:          s.S3Bucket(),
		S3PublicURLPrefix: s.S3PublicURLPrefix(),
		EthNodeAvailable:  s.EthNodeAvailable(),
	}

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
	// Extract port from listen address.
	port := "18081"
	if _, p, err := net.SplitHostPort(s.cfg.Server.ListenAddr); err == nil && p != "" {
		port = p
	}

	return fmt.Sprintf("http://localhost:%s", port)
}

// ClickHouseDatasources returns the list of ClickHouse datasource names.
func (s *server) ClickHouseDatasources() []string {
	if s.clickhouseHandler == nil {
		return nil
	}

	return s.clickhouseHandler.Clusters()
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
