package proxy

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
	"github.com/ethpandaops/panda/pkg/proxy/handlers"
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
	authorizer    *Authorizer
	rateLimiter   *RateLimiter
	auditor       *Auditor

	clickhouseHandler *handlers.ClickHouseHandler
	prometheusHandler *handlers.PrometheusHandler
	lokiHandler       *handlers.LokiHandler
	ethNodeHandler    *handlers.EthNodeHandler
	embeddingService  *EmbeddingService

	mu      sync.RWMutex
	started bool
}

// Compile-time interface check.
var (
	_ Server  = (*server)(nil)
	_ Service = (*server)(nil)
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
			Enabled:         true,
			IssuerURL:       cfg.Auth.IssuerURL,
			GitHub:          cfg.Auth.GitHub,
			AllowedOrgs:     append([]string(nil), cfg.Auth.AllowedOrgs...),
			Tokens:          cfg.Auth.Tokens,
			AccessTokenTTL:  cfg.Auth.AccessTokenTTL,
			RefreshTokenTTL: cfg.Auth.RefreshTokenTTL,
			SuccessPage:     cfg.Auth.SuccessPage,
		}

		authSvc, err := simpleauth.NewSimpleService(log, authCfg)
		if err != nil {
			return nil, fmt.Errorf("creating proxy auth service: %w", err)
		}

		s.authService = authSvc
		s.authenticator = NewSimpleServiceAuthenticator(authSvc)
	case AuthModeOIDC:
		oidcAuth, err := NewOIDCAuthenticator(log, OIDCAuthenticatorConfig{
			IssuerURL: cfg.Auth.IssuerURL,
			ClientID:  cfg.Auth.ClientID,
		})
		if err != nil {
			return nil, fmt.Errorf("creating OIDC authenticator: %w", err)
		}

		s.authenticator = oidcAuth
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
		s.auditor = NewAuditor(log, AuditorConfig{})
	}

	// Create authorizer for per-datasource access control.
	s.authorizer = NewAuthorizer(log, cfg)

	// Create handlers from config.
	chConfigs, promConfigs, lokiConfigs, ethNodeConfig := cfg.ToHandlerConfigs()

	if len(chConfigs) > 0 {
		s.clickhouseHandler = handlers.NewClickHouseHandler(log, chConfigs)
	}

	if len(promConfigs) > 0 {
		s.prometheusHandler = handlers.NewPrometheusHandler(log, promConfigs)
	}

	if len(lokiConfigs) > 0 {
		s.lokiHandler = handlers.NewLokiHandler(log, lokiConfigs)
	}

	if ethNodeConfig != nil {
		s.ethNodeHandler = handlers.NewEthNodeHandler(log, *ethNodeConfig)
	}

	// Create embedding service if configured.
	if cfg.Embedding != nil {
		embCache, err := buildEmbeddingCache(cfg.Embedding.Cache)
		if err != nil {
			return nil, fmt.Errorf("creating embedding cache: %w", err)
		}

		s.embeddingService = NewEmbeddingService(
			log,
			embCache,
			cfg.Embedding.APIKey,
			cfg.Embedding.Model,
			cfg.Embedding.APIURL,
			0,
		)
	}

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

	// Branding endpoint (no auth required) — serves success_page config as JSON.
	s.mux.HandleFunc("/auth/branding", s.handleBranding)

	// Auth metadata endpoint (no auth required) — lets clients discover auth settings.
	s.mux.HandleFunc("/auth/metadata", s.handleAuthMetadata)

	// Datasources info endpoint (for discovery by MCP server and Python modules).
	// Build middleware chain.
	chain := s.buildMiddlewareChain()

	if s.authService != nil {
		s.authService.MountRoutes(s.mux)
	}

	s.mux.Handle("/datasources", s.metricsMiddleware(chain(http.HandlerFunc(s.handleDatasources))))

	if s.embeddingService != nil {
		s.mux.Method(http.MethodPost, "/embed", s.metricsMiddleware(chain(http.HandlerFunc(s.handleEmbed))))
		s.mux.Method(http.MethodPost, "/embed/check", s.metricsMiddleware(chain(http.HandlerFunc(s.handleEmbedCheck))))
	}

	// Authenticated routes.
	if s.clickhouseHandler != nil {
		s.handleSubtreeRoute("/clickhouse", s.metricsMiddleware(chain(s.clickhouseHandler)))
	}

	if s.prometheusHandler != nil {
		s.handleSubtreeRoute("/prometheus", s.metricsMiddleware(chain(s.prometheusHandler)))
	}

	if s.lokiHandler != nil {
		s.handleSubtreeRoute("/loki", s.metricsMiddleware(chain(s.lokiHandler)))
	}

	if s.ethNodeHandler != nil {
		s.handleSubtreeRoute("/beacon", s.metricsMiddleware(chain(s.ethNodeHandler)))
		s.handleSubtreeRoute("/execution", s.metricsMiddleware(chain(s.ethNodeHandler)))
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

		// Authorization (per-datasource org check).
		if s.authorizer != nil {
			h = s.authorizer.Middleware()(h)
		}

		// Authentication (outermost).
		h = s.authenticator.Middleware()(h)

		return h
	}
}

// DatasourcesResponse is the response from the /datasources endpoint.
// This is used by the MCP server client to discover available datasources.
type DatasourcesResponse struct {
	ClickHouse         []string               `json:"clickhouse,omitempty"`
	Prometheus         []string               `json:"prometheus,omitempty"`
	Loki               []string               `json:"loki,omitempty"`
	ClickHouseInfo     []types.DatasourceInfo `json:"clickhouse_info,omitempty"`
	PrometheusInfo     []types.DatasourceInfo `json:"prometheus_info,omitempty"`
	LokiInfo           []types.DatasourceInfo `json:"loki_info,omitempty"`
	EthNodeAvailable   bool                   `json:"ethnode_available,omitempty"`
	EmbeddingAvailable bool                   `json:"embedding_available,omitempty"`
	EmbeddingModel     string                 `json:"embedding_model,omitempty"`
}

// handleDatasources returns the list of available datasources,
// filtered by the authenticated user's org membership.
func (s *server) handleDatasources(w http.ResponseWriter, r *http.Request) {
	info := DatasourcesResponse{
		ClickHouse:         s.ClickHouseDatasources(),
		Prometheus:         s.PrometheusDatasources(),
		Loki:               s.LokiDatasources(),
		ClickHouseInfo:     s.ClickHouseDatasourceInfo(),
		PrometheusInfo:     s.PrometheusDatasourceInfo(),
		LokiInfo:           s.LokiDatasourceInfo(),
		EthNodeAvailable:   s.EthNodeAvailable(),
		EmbeddingAvailable: s.EmbeddingAvailable(),
		EmbeddingModel:     s.EmbeddingModel(),
	}

	if s.authorizer != nil {
		info = s.authorizer.FilterDatasources(r.Context(), info)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(info); err != nil {
		s.log.WithError(err).Error("Failed to encode datasources response")
	}
}

// handleEmbed handles embedding requests by delegating to the embedding service.
func (s *server) handleEmbed(w http.ResponseWriter, r *http.Request) {
	var req EmbedRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request body: %v", err), http.StatusBadRequest)

		return
	}

	if len(req.Items) > maxEmbedItems {
		http.Error(w, fmt.Sprintf("too many items: %d exceeds maximum of %d", len(req.Items), maxEmbedItems), http.StatusBadRequest)

		return
	}

	resp, err := s.embeddingService.Embed(r.Context(), req.Items)
	if err != nil {
		s.log.WithError(err).Error("Embedding request failed")
		http.Error(w, fmt.Sprintf("embedding failed: %v", err), http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.log.WithError(err).Error("Failed to encode embedding response")
	}
}

// handleEmbedCheck returns cached vectors for the given hashes without embedding new content.
func (s *server) handleEmbedCheck(w http.ResponseWriter, r *http.Request) {
	var req EmbedCheckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request body: %v", err), http.StatusBadRequest)

		return
	}

	results, err := s.embeddingService.CheckCached(r.Context(), req.Hashes)
	if err != nil {
		s.log.WithError(err).Error("Embed check failed")
		http.Error(w, fmt.Sprintf("embed check failed: %v", err), http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(EmbedCheckResponse{Cached: results}); err != nil {
		s.log.WithError(err).Error("Failed to encode embed check response")
	}
}

// handleBranding returns the success_page config as JSON, or 204 if not configured.
func (s *server) handleBranding(w http.ResponseWriter, _ *http.Request) {
	if s.cfg.Auth.SuccessPage == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(s.cfg.Auth.SuccessPage); err != nil {
		s.log.WithError(err).Error("Failed to encode branding response")
	}
}

// AuthMetadataResponse describes the proxy's auth configuration for client discovery.
type AuthMetadataResponse struct {
	Enabled   bool   `json:"enabled"`
	Mode      string `json:"mode"`
	IssuerURL string `json:"issuer_url,omitempty"`
	ClientID  string `json:"client_id,omitempty"`
}

// handleAuthMetadata returns the proxy's auth config so clients can discover
// the correct issuer URL and client ID without hardcoding them.
func (s *server) handleAuthMetadata(w http.ResponseWriter, _ *http.Request) {
	resp := AuthMetadataResponse{
		Enabled: s.cfg.Auth.Mode != AuthModeNone,
		Mode:    string(s.cfg.Auth.Mode),
	}

	if resp.Enabled {
		resp.IssuerURL = s.cfg.Auth.IssuerURL
		resp.ClientID = s.cfg.Auth.ClientID
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.log.WithError(err).Error("Failed to encode auth metadata response")
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

	// Close embedding service.
	if s.embeddingService != nil {
		if err := s.embeddingService.Close(); err != nil {
			s.log.WithError(err).Warn("Error closing embedding service")
		}
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

func (s *server) RegisterToken(executionID string) string {
	return "none"
}

func (s *server) RevokeToken(executionID string) {
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

// EthNodeAvailable returns true if the ethnode handler is configured.
func (s *server) EthNodeAvailable() bool {
	return s.ethNodeHandler != nil
}

// EmbeddingAvailable returns true if the embedding service is configured.
func (s *server) EmbeddingAvailable() bool {
	return s.embeddingService != nil
}

// EmbeddingModel returns the configured embedding model name.
func (s *server) EmbeddingModel() string {
	if s.embeddingService == nil {
		return ""
	}

	return s.embeddingService.Model()
}

func advertisedURLs(listenAddr string) (string, string) {
	port := "18081"
	if _, p, err := net.SplitHostPort(listenAddr); err == nil && p != "" {
		port = p
	}

	url := fmt.Sprintf("http://localhost:%s", port)

	return url, port
}
