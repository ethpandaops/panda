package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/auth"
	authclient "github.com/ethpandaops/panda/pkg/auth/client"
	"github.com/ethpandaops/panda/pkg/proxy/handlers"
	"github.com/ethpandaops/panda/pkg/types"
)

const (
	// githubTriggerBurst is the cross-workflow trigger budget: this many
	// workflow_dispatch triggers may fire before the budget throttles.
	githubTriggerBurst = 10
	// githubTriggerRefillPerMinute is how fast the trigger budget refills.
	// Ten per ten minutes refills at one per minute.
	githubTriggerRefillPerMinute = 1

	// uploadsBurst is the per-user upload budget: this many uploads may fire
	// back-to-back (e.g. `panda upload *.png`) before the budget throttles.
	uploadsBurst = 20
	// uploadsRefillPerMinute caps sustained per-user uploads to one per second.
	uploadsRefillPerMinute = 60
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

	authenticator        Authenticator
	authService          auth.AuthorizationServer
	authorizer           *Authorizer
	rateLimiter          *RateLimiter
	githubTriggerLimiter *RateLimiter
	auditor              *Auditor

	clickhouseHandler   *handlers.ClickHouseHandler
	prometheusHandler   *handlers.PrometheusHandler
	lokiHandler         *handlers.LokiHandler
	ethNodeHandler      *handlers.EthNodeHandler
	benchmarkoorHandler *handlers.BenchmarkoorHandler
	computeHandler      *handlers.ComputeHandler
	workflowHandler     *handlers.WorkflowHandler
	uploadsHandler      *handlers.UploadsHandler
	uploadsLimiter      *RateLimiter
	embeddingService    *EmbeddingService
	embeddingServiceV2  *EmbeddingService
	githubHandler       *handlers.GitHubHandler

	autodiscoverHTTPClient  *http.Client
	autodiscoverCancel      context.CancelFunc
	autodiscoverWG          sync.WaitGroup
	autodiscoverMu          sync.Mutex
	staticClickHouseNames   map[string]struct{}
	dynamicClickHouseNames  map[string]struct{}
	staticAutodiscoverWarns map[string]struct{}

	mu        sync.RWMutex
	started   bool
	serveDone chan struct{}
}

// Compile-time interface check.
var (
	_ Server               = (*server)(nil)
	_ Service              = (*server)(nil)
	_ WorkflowInfoProvider = (*server)(nil)
)

// NewServer creates a new proxy server.
func NewServer(log logrus.FieldLogger, cfg ServerConfig) (Server, error) {
	hostURL, port := advertisedURLs(cfg.Server.ListenAddr)

	return newServer(log, cfg, hostURL, port)
}

func newServer(log logrus.FieldLogger, cfg ServerConfig, hostURL, port string) (*server, error) {
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validating proxy config: %w", err)
	}

	s := &server{
		log: log.WithField("component", "proxy"),
		cfg: cfg,
		mux: chi.NewRouter(),
		url: hostURL,
		autodiscoverHTTPClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}

	// Create authenticator based on mode.
	switch cfg.Auth.Mode {
	case AuthModeNone:
		s.authenticator = NewNoneAuthenticator(log)
	case AuthModeOAuth:
		authCfg := auth.Config{
			Enabled:         true,
			IssuerURL:       cfg.Auth.IssuerURL,
			GitHub:          cfg.Auth.GitHub,
			AllowedOrgs:     append([]string(nil), cfg.Auth.AllowedOrgs...),
			Tokens:          cfg.Auth.Tokens,
			AccessTokenTTL:  cfg.Auth.AccessTokenTTL,
			RefreshTokenTTL: cfg.Auth.RefreshTokenTTL,
			SuccessPage:     cfg.Auth.SuccessPage,
		}

		authSvc, err := auth.NewAuthorizationServer(log, authCfg)
		if err != nil {
			return nil, fmt.Errorf("creating proxy auth service: %w", err)
		}

		s.authService = authSvc
		s.authenticator = NewAuthServerAuthenticator(authSvc)
	case AuthModeOIDC:
		oidcAuth, err := NewOIDCAuthenticator(log, OIDCAuthenticatorConfig{
			Issuers: cfg.Auth.Issuers,
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
		s.auditor = NewAuditor(log, cfg.Audit)
	}

	// Create authorizer for per-datasource access control.
	s.authorizer = NewAuthorizer(log, cfg)

	// Create handlers from config.
	chConfigs, promConfigs, lokiConfigs, ethNodeConfig := cfg.ToHandlerConfigs()
	s.staticClickHouseNames = clickHouseConfigNameSet(chConfigs)
	s.dynamicClickHouseNames = make(map[string]struct{})
	s.staticAutodiscoverWarns = make(map[string]struct{})

	if len(chConfigs) > 0 || hasClickHouseAutodiscover(cfg.ClickHouse) {
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

	if benchConfigs := cfg.ToBenchmarkoorHandlerConfigs(); len(benchConfigs) > 0 {
		s.benchmarkoorHandler = handlers.NewBenchmarkoorHandler(log, benchConfigs)
	}

	if computeConfigs := cfg.ToComputeHandlerConfigs(); len(computeConfigs) > 0 {
		s.computeHandler = handlers.NewComputeHandler(log, computeConfigs)
	}

	if cfg.Workflow != nil {
		workflowHandler, err := handlers.NewWorkflowHandler(log, handlers.WorkflowConfig{
			URL:      cfg.Workflow.URL,
			AuthMode: cfg.Workflow.ResolvedAuthMode(),
			APIToken: cfg.Workflow.APIToken,
		})
		if err != nil {
			return nil, fmt.Errorf("creating workflow handler: %w", err)
		}

		s.workflowHandler = workflowHandler
	}

	if uploadsCfg := cfg.ToUploadsHandlerConfig(); uploadsCfg != nil {
		uploadsHandler, err := handlers.NewUploadsHandler(log, *uploadsCfg)
		if err != nil {
			return nil, fmt.Errorf("creating uploads handler: %w", err)
		}

		s.uploadsHandler = uploadsHandler
		s.uploadsLimiter = NewRateLimiter(log, RateLimiterConfig{
			RequestsPerMinute: uploadsRefillPerMinute,
			BurstSize:         uploadsBurst,
		})
	}

	// Create embedding service if configured.
	if cfg.Embedding != nil {
		if cfg.Embedding.Dimensions != 0 {
			log.WithFields(logrus.Fields{
				"dimensions": cfg.Embedding.Dimensions,
				"model":      cfg.Embedding.Model,
			}).Warn("embedding.dimensions is ignored by v1 embedding; use embedding_v2")
		}

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

	// Create v2/v3 embedding service if configured.
	if cfg.EmbeddingV2 != nil {
		embCacheV2, err := buildEmbeddingCache(cfg.EmbeddingV2.Cache)
		if err != nil {
			return nil, fmt.Errorf("creating v2 embedding cache: %w", err)
		}

		s.embeddingServiceV2 = NewEmbeddingServiceWithDimensions(
			log,
			embCacheV2,
			cfg.EmbeddingV2.APIKey,
			cfg.EmbeddingV2.Model,
			cfg.EmbeddingV2.APIURL,
			0,
			cfg.EmbeddingV2.Dimensions,
		)
	}

	// Create GitHub handler if configured.
	if cfg.GitHub != nil && cfg.GitHub.Token != "" {
		s.githubTriggerLimiter = NewRateLimiter(log, RateLimiterConfig{
			RequestsPerMinute: githubTriggerRefillPerMinute,
			BurstSize:         githubTriggerBurst,
		})

		s.githubHandler = handlers.NewGitHubHandler(log, handlers.GitHubConfig{
			Token: cfg.GitHub.Token,
		}, s.githubTriggerLimiter)
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

	if s.embeddingServiceV2 != nil {
		s.mux.Method(http.MethodPost, "/v2/embedding", s.metricsMiddleware(chain(http.HandlerFunc(s.handleEmbedV2))))
		s.mux.Method(http.MethodPost, "/v2/embedding/check", s.metricsMiddleware(chain(http.HandlerFunc(s.handleEmbedCheckV2))))
		s.mux.Method(http.MethodPost, "/v3/embedding", s.metricsMiddleware(chain(http.HandlerFunc(s.handleEmbedV3))))
		s.mux.Method(http.MethodPost, "/v3/embedding/check", s.metricsMiddleware(chain(http.HandlerFunc(s.handleEmbedCheckV3))))
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

	if s.benchmarkoorHandler != nil {
		s.handleSubtreeRoute("/benchmarkoor", s.metricsMiddleware(chain(s.benchmarkoorHandler)))
	}

	if s.computeHandler != nil {
		s.handleSubtreeRoute("/compute", s.metricsMiddleware(chain(s.computeHandler)))
	}

	if s.workflowHandler != nil {
		s.handleSubtreeRoute("/workflow", s.metricsMiddleware(chain(s.workflowHandler)))
	}

	if s.githubHandler != nil {
		s.handleSubtreeRoute("/github", s.metricsMiddleware(chain(s.githubHandler)))
	}

	if s.uploadsHandler != nil {
		// A dedicated per-user limiter (keyed on the authenticated subject, so it
		// runs inside chain() after auth) throttles uploads tighter than the
		// generic request limiter, since each upload writes bytes to R2.
		uploads := http.Handler(s.uploadsHandler)
		if s.uploadsLimiter != nil {
			uploads = s.uploadsLimiter.Middleware()(uploads)
		}

		s.handleSubtreeRoute("/uploads", s.metricsMiddleware(chain(uploads)))
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

		// Rate limiting (innermost).
		if s.rateLimiter != nil {
			h = s.rateLimiter.Middleware()(h)
		}

		// Authorization (per-datasource org check).
		if s.authorizer != nil {
			h = s.authorizer.Middleware()(h)
		}

		// Audit logging (after auth, wraps authorizer and rate limiter
		// so that 403s and 429s are captured in the audit log).
		if s.auditor != nil {
			h = s.auditor.Middleware()(h)
		}

		// Authentication (outermost).
		h = s.authenticator.Middleware()(h)

		return h
	}
}

// DatasourcesResponse is the response from the /datasources endpoint.
// This is used by the MCP server client to discover available datasources.
//
// Datasource identity is carried solely by the *Info fields. The wire format
// additionally emits and accepts a parallel name-only list per type
// (clickhouse/prometheus/loki) for compatibility with older peers; that list
// is derived from the *Info fields and never stored separately.
type DatasourcesResponse struct {
	ClickHouseInfo     []types.DatasourceInfo `json:"clickhouse_info,omitempty"`
	PrometheusInfo     []types.DatasourceInfo `json:"prometheus_info,omitempty"`
	LokiInfo           []types.DatasourceInfo `json:"loki_info,omitempty"`
	BenchmarkoorInfo   []types.DatasourceInfo `json:"benchmarkoor_info,omitempty"`
	ComputeInfo        []types.DatasourceInfo `json:"compute_info,omitempty"`
	EthNodeAvailable   bool                   `json:"ethnode_available,omitempty"`
	EmbeddingAvailable bool                   `json:"embedding_available,omitempty"`
	EmbeddingModel     string                 `json:"embedding_model,omitempty"`
	// Workflow advertises the workflow engine as a capability. Nil when the
	// proxy does not expose one (or the caller is not authorized for it).
	Workflow *WorkflowAdvert `json:"workflow,omitempty"`
}

// WorkflowAdvert advertises the workflow engine capability in discovery. It
// carries only whether the engine is enabled and the web URL for building human
// links — never the api_token.
type WorkflowAdvert struct {
	Enabled bool   `json:"enabled"`
	WebURL  string `json:"web_url,omitempty"`
}

// datasourcesResponseWire is the on-the-wire shape of DatasourcesResponse,
// carrying both the name-only lists and the detailed *Info lists.
type datasourcesResponseWire struct {
	ClickHouse         []string               `json:"clickhouse,omitempty"`
	Prometheus         []string               `json:"prometheus,omitempty"`
	Loki               []string               `json:"loki,omitempty"`
	ClickHouseInfo     []types.DatasourceInfo `json:"clickhouse_info,omitempty"`
	PrometheusInfo     []types.DatasourceInfo `json:"prometheus_info,omitempty"`
	LokiInfo           []types.DatasourceInfo `json:"loki_info,omitempty"`
	BenchmarkoorInfo   []types.DatasourceInfo `json:"benchmarkoor_info,omitempty"`
	ComputeInfo        []types.DatasourceInfo `json:"compute_info,omitempty"`
	EthNodeAvailable   bool                   `json:"ethnode_available,omitempty"`
	EmbeddingAvailable bool                   `json:"embedding_available,omitempty"`
	EmbeddingModel     string                 `json:"embedding_model,omitempty"`
	Workflow           *WorkflowAdvert        `json:"workflow,omitempty"`
}

// MarshalJSON emits both the detailed *Info lists and the derived name-only
// lists so older peers that only read the name lists continue to work.
func (d DatasourcesResponse) MarshalJSON() ([]byte, error) {
	return json.Marshal(datasourcesResponseWire{
		ClickHouse:         datasourceNames(d.ClickHouseInfo),
		Prometheus:         datasourceNames(d.PrometheusInfo),
		Loki:               datasourceNames(d.LokiInfo),
		ClickHouseInfo:     d.ClickHouseInfo,
		PrometheusInfo:     d.PrometheusInfo,
		LokiInfo:           d.LokiInfo,
		BenchmarkoorInfo:   d.BenchmarkoorInfo,
		ComputeInfo:        d.ComputeInfo,
		EthNodeAvailable:   d.EthNodeAvailable,
		EmbeddingAvailable: d.EmbeddingAvailable,
		EmbeddingModel:     d.EmbeddingModel,
		Workflow:           d.Workflow,
	})
}

// UnmarshalJSON reads the detailed *Info lists when present and otherwise
// reconstructs them from the name-only lists emitted by older peers.
func (d *DatasourcesResponse) UnmarshalJSON(data []byte) error {
	var wire datasourcesResponseWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}

	d.ClickHouseInfo = infoFromWire("clickhouse", wire.ClickHouseInfo, wire.ClickHouse)
	d.PrometheusInfo = infoFromWire("prometheus", wire.PrometheusInfo, wire.Prometheus)
	d.LokiInfo = infoFromWire("loki", wire.LokiInfo, wire.Loki)
	d.BenchmarkoorInfo = normalizeInfo("benchmarkoor", wire.BenchmarkoorInfo, "")
	d.ComputeInfo = normalizeInfo("compute", wire.ComputeInfo, "")
	d.EthNodeAvailable = wire.EthNodeAvailable
	d.EmbeddingAvailable = wire.EmbeddingAvailable
	d.EmbeddingModel = wire.EmbeddingModel
	d.Workflow = wire.Workflow

	return nil
}

// datasourceNames returns the non-empty names from a list of DatasourceInfo.
func datasourceNames(infos []types.DatasourceInfo) []string {
	if len(infos) == 0 {
		return nil
	}

	names := make([]string, 0, len(infos))
	for _, info := range infos {
		if info.Name != "" {
			names = append(names, info.Name)
		}
	}

	return names
}

// infoFromWire prefers the detailed info list and falls back to synthesizing
// info entries from a name-only list (older peers).
func infoFromWire(kind string, infos []types.DatasourceInfo, names []string) []types.DatasourceInfo {
	if len(infos) > 0 {
		return normalizeInfo(kind, infos, "")
	}

	return namesToInfo(kind, names, "")
}

// handleDatasources returns the list of available datasources,
// filtered by the authenticated user's org membership.
func (s *server) handleDatasources(w http.ResponseWriter, r *http.Request) {
	info := DatasourcesResponse{
		ClickHouseInfo:     s.ClickHouseDatasourceInfo(),
		PrometheusInfo:     s.PrometheusDatasourceInfo(),
		LokiInfo:           s.LokiDatasourceInfo(),
		BenchmarkoorInfo:   s.BenchmarkoorDatasourceInfo(),
		ComputeInfo:        s.ComputeDatasourceInfo(),
		EthNodeAvailable:   s.EthNodeAvailable(),
		EmbeddingAvailable: s.EmbeddingAvailable(),
		EmbeddingModel:     s.EmbeddingModel(),
		Workflow:           s.workflowAdvert(),
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

// handleEmbed handles v1 embedding requests by delegating to the v1 service.
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

	resp, err := s.embeddingService.Embed(r.Context(), req.Items, "")
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

// handleEmbedCheck returns cached v1 vectors for the given hashes without
// embedding new content.
func (s *server) handleEmbedCheck(w http.ResponseWriter, r *http.Request) {
	var req EmbedCheckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request body: %v", err), http.StatusBadRequest)

		return
	}

	if len(req.Hashes) > maxEmbedItems {
		http.Error(w, fmt.Sprintf("too many hashes: %d exceeds maximum of %d", len(req.Hashes), maxEmbedItems), http.StatusBadRequest)

		return
	}

	results, err := s.embeddingService.CheckCached(r.Context(), req.Hashes, "")
	if err != nil {
		s.log.WithError(err).Error("Embed check failed")
		http.Error(w, fmt.Sprintf("embed check failed: %v", err), http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(EmbedCheckResponse{
		Model:  s.embeddingService.Model(),
		Cached: results,
	}); err != nil {
		s.log.WithError(err).Error("Failed to encode embed check response")
	}
}

// handleEmbedV2 handles symmetric fixed-dimensional embedding requests.
func (s *server) handleEmbedV2(w http.ResponseWriter, r *http.Request) {
	var req EmbedRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request body: %v", err), http.StatusBadRequest)

		return
	}

	if len(req.Items) > maxEmbedItems {
		http.Error(w, fmt.Sprintf("too many items: %d exceeds maximum of %d", len(req.Items), maxEmbedItems), http.StatusBadRequest)

		return
	}

	resp, err := s.embeddingServiceV2.Embed(r.Context(), req.Items, "")
	if err != nil {
		s.log.WithError(err).Error("V2 embedding request failed")
		http.Error(w, fmt.Sprintf("embedding failed: %v", err), http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.log.WithError(err).Error("Failed to encode v2 embedding response")
	}
}

// handleEmbedCheckV2 returns cached v2 vectors for the given hashes.
func (s *server) handleEmbedCheckV2(w http.ResponseWriter, r *http.Request) {
	var req EmbedCheckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request body: %v", err), http.StatusBadRequest)

		return
	}

	if len(req.Hashes) > maxEmbedItems {
		http.Error(w, fmt.Sprintf("too many hashes: %d exceeds maximum of %d", len(req.Hashes), maxEmbedItems), http.StatusBadRequest)

		return
	}

	results, err := s.embeddingServiceV2.CheckCached(r.Context(), req.Hashes, "")
	if err != nil {
		s.log.WithError(err).Error("V2 embed check failed")
		http.Error(w, fmt.Sprintf("embed check failed: %v", err), http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(EmbedCheckResponse{
		Model:      s.embeddingServiceV2.Model(),
		Dimensions: s.embeddingServiceV2.Dimensions(),
		Cached:     results,
	}); err != nil {
		s.log.WithError(err).Error("Failed to encode v2 embed check response")
	}
}

// handleEmbedV3 handles task-typed fixed-dimensional embedding requests.
func (s *server) handleEmbedV3(w http.ResponseWriter, r *http.Request) {
	var req EmbedRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request body: %v", err), http.StatusBadRequest)

		return
	}

	if len(req.Items) > maxEmbedItems {
		http.Error(w, fmt.Sprintf("too many items: %d exceeds maximum of %d", len(req.Items), maxEmbedItems), http.StatusBadRequest)

		return
	}

	if _, _, err := embedTaskParams(req.Task); err != nil || req.Task == "" {
		if err == nil {
			err = fmt.Errorf("embed task is required and must be %q or %q", EmbedTaskQuery, EmbedTaskDocument)
		}

		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	resp, err := s.embeddingServiceV2.Embed(r.Context(), req.Items, req.Task)
	if err != nil {
		s.log.WithError(err).Error("V3 embedding request failed")
		http.Error(w, fmt.Sprintf("embedding failed: %v", err), http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.log.WithError(err).Error("Failed to encode v3 embedding response")
	}
}

// handleEmbedCheckV3 returns cached task-scoped v3 vectors for the given hashes.
func (s *server) handleEmbedCheckV3(w http.ResponseWriter, r *http.Request) {
	var req EmbedCheckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request body: %v", err), http.StatusBadRequest)

		return
	}

	if len(req.Hashes) > maxEmbedItems {
		http.Error(w, fmt.Sprintf("too many hashes: %d exceeds maximum of %d", len(req.Hashes), maxEmbedItems), http.StatusBadRequest)

		return
	}

	if _, _, err := embedTaskParams(req.Task); err != nil || req.Task == "" {
		if err == nil {
			err = fmt.Errorf("embed task is required and must be %q or %q", EmbedTaskQuery, EmbedTaskDocument)
		}

		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	results, err := s.embeddingServiceV2.CheckCached(r.Context(), req.Hashes, req.Task)
	if err != nil {
		s.log.WithError(err).Error("V3 embed check failed")
		http.Error(w, fmt.Sprintf("embed check failed: %v", err), http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(EmbedCheckResponse{
		Model:      s.embeddingServiceV2.Model(),
		Dimensions: s.embeddingServiceV2.Dimensions(),
		Cached:     results,
	}); err != nil {
		s.log.WithError(err).Error("Failed to encode v3 embed check response")
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
	// Scopes is the complete scope set clients should request at login to use
	// this proxy fully. Empty means "use the client defaults". It carries the
	// extra scopes required by advertised features (e.g. "workflows" when a
	// workflow engine is configured) so `panda init` can provision them.
	Scopes []string `json:"scopes,omitempty"`
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

		// In oidc mode the trusted issuers live in Auth.Issuers; advertise the
		// first (interactive) one so clients know where to log in.
		if s.cfg.Auth.Mode == AuthModeOIDC && len(s.cfg.Auth.Issuers) > 0 {
			resp.IssuerURL = s.cfg.Auth.Issuers[0].IssuerURL
			resp.ClientID = s.cfg.Auth.Issuers[0].ClientID
		}

		// Advertise the extra scopes advertised features require so `panda init`
		// provisions them. The "workflows" scope matters only when the caller's
		// own token reaches the engine — oidc mode with workflow passthrough
		// auth, where Authentik cross-grants the engine audience to eligible
		// users (safe to request for everyone). In token mode the proxy injects
		// its api_token, and in oauth (GitHub) mode these OIDC scope names would
		// be meaningless, so both keep the client defaults.
		if s.cfg.Auth.Mode == AuthModeOIDC &&
			s.cfg.Workflow != nil && strings.TrimSpace(s.cfg.Workflow.URL) != "" &&
			s.cfg.Workflow.ResolvedAuthMode() == WorkflowAuthModePassthrough {
			resp.Scopes = append(append([]string(nil), authclient.DefaultScopes...), "workflows")
		}
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
	if listenPortIsEphemeral(s.cfg.Server.ListenAddr) {
		s.url = urlFromListenerAddr(listener.Addr(), s.url)
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
	s.serveDone = make(chan struct{})

	// Start server in background with the already-bound listener.
	go func() {
		defer close(s.serveDone)
		if err := s.httpSrv.Serve(listener); err != nil && err != http.ErrServerClosed {
			s.log.WithError(err).Error("Proxy server error")
		}
	}()

	s.startAutodiscoveryLocked(ctx)
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

	s.stopAutodiscoveryLocked()

	// Stop authenticator.
	if err := s.authenticator.Stop(); err != nil {
		s.log.WithError(err).Warn("Error stopping authenticator")
	}

	// Stop rate limiters.
	if s.rateLimiter != nil {
		s.rateLimiter.Stop()
	}

	if s.githubTriggerLimiter != nil {
		s.githubTriggerLimiter.Stop()
	}

	if s.uploadsLimiter != nil {
		s.uploadsLimiter.Stop()
	}

	// Close embedding service.
	if s.embeddingService != nil {
		if err := s.embeddingService.Close(); err != nil {
			s.log.WithError(err).Warn("Error closing embedding service")
		}
	}

	if s.embeddingServiceV2 != nil {
		if err := s.embeddingServiceV2.Close(); err != nil {
			s.log.WithError(err).Warn("Error closing v2 embedding service")
		}
	}

	// Shutdown HTTP server.
	if s.httpSrv != nil {
		if err := s.httpSrv.Shutdown(ctx); err != nil {
			return fmt.Errorf("shutting down proxy server: %w", err)
		}
	}
	if s.serveDone != nil {
		select {
		case <-s.serveDone:
		case <-ctx.Done():
			return fmt.Errorf("waiting for proxy server shutdown: %w", ctx.Err())
		}
	}

	s.started = false
	s.log.Info("Proxy server stopped")

	return nil
}

// URL returns the proxy URL.
func (s *server) URL() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.url
}

func (s *server) RegisterToken() string {
	return NoAuthToken
}

// Ready reports whether the embedded proxy server has finished starting. It
// satisfies the proxy.Service readiness contract for in-process proxies.
func (s *server) Ready() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.started
}

// Invalidate is a no-op: the embedded proxy issues no bearer tokens.
func (s *server) Invalidate() {
}

func (s *server) RevokeToken() {
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
	if s.clickhouseHandler == nil {
		return nil
	}

	result := make([]types.DatasourceInfo, 0, len(s.clickhouseHandler.Clusters()))
	seen := make(map[string]struct{}, len(s.cfg.ClickHouse))

	for _, ch := range s.cfg.ClickHouse {
		if ch.Autodiscover {
			continue
		}

		info := types.DatasourceInfo{
			Type:        "clickhouse",
			Name:        ch.Name,
			Description: ch.Description,
			Contents:    bindingsToInfo(ch.Contains),
		}
		info.Metadata = metadataValue("database", ch.Database)
		if len(ch.Variants) > 0 {
			info.Metadata = metadataValue("database", ch.Variants[0].Database)
		}
		result = append(result, info)
		seen[ch.Name] = struct{}{}
	}

	// Autodiscovered clusters keep their configured name, so their dataset
	// bindings come from the matching config entry.
	containsByName := make(map[string][]types.DatasetBinding, len(s.cfg.ClickHouse))

	for _, ch := range s.cfg.ClickHouse {
		if len(ch.Contains) > 0 {
			containsByName[ch.Name] = bindingsToInfo(ch.Contains)
		}
	}

	for _, name := range s.clickhouseHandler.Clusters() {
		if _, ok := seen[name]; ok {
			continue
		}

		cfg, ok := s.clickhouseHandler.ClusterConfig(name)
		if !ok {
			continue
		}

		result = append(result, types.DatasourceInfo{
			Type:        "clickhouse",
			Name:        cfg.Name,
			Description: cfg.Description,
			Metadata:    metadataValue("database", cfg.Database),
			Contents:    containsByName[cfg.Name],
		})
	}

	return result
}

// ClickHouseQuery runs a ClickHouse SQL query against the named datasource by
// dispatching through the proxy's own /clickhouse/ route, so the same auth,
// authorization, and credential injection apply as for external callers.
func (s *server) ClickHouseQuery(ctx context.Context, datasource, sql string, params url.Values) ([]byte, error) {
	if datasource == "" {
		return nil, fmt.Errorf("datasource name is required")
	}

	if s.cfg.Auth.Mode != AuthModeNone {
		return nil, fmt.Errorf(
			"in-process ClickHouseQuery requires proxy auth.mode=%q; configured auth.mode=%q cannot supply a request identity",
			AuthModeNone,
			s.cfg.Auth.Mode,
		)
	}

	requestURL := "/clickhouse/"
	if encoded := params.Encode(); encoded != "" {
		requestURL += "?" + encoded
	}

	req := httptest.NewRequest(http.MethodPost, requestURL, strings.NewReader(sql)).WithContext(ctx)
	req.Header.Set(handlers.DatasourceHeader, datasource)
	req.Header.Set("Content-Type", "text/plain")

	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	body := rec.Body.Bytes()
	if rec.Code < 200 || rec.Code >= 300 {
		return nil, fmt.Errorf("query failed (%d): %s", rec.Code, strings.TrimSpace(string(body)))
	}

	return body, nil
}

// PrometheusDatasources returns the list of Prometheus datasource names.
func (s *server) PrometheusDatasources() []string {
	if len(s.cfg.Prometheus) == 0 {
		return nil
	}

	names := make([]string, 0, len(s.cfg.Prometheus))
	for _, cfg := range s.cfg.Prometheus {
		names = append(names, cfg.Name)
	}

	return names
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
		info.Metadata = metadataValue("url", prom.URL)
		if len(prom.Variants) > 0 {
			info.Metadata = metadataValue("url", prom.Variants[0].URL)
		}
		result = append(result, info)
	}

	return result
}

// LokiDatasources returns the list of Loki datasource names.
func (s *server) LokiDatasources() []string {
	if len(s.cfg.Loki) == 0 {
		return nil
	}

	names := make([]string, 0, len(s.cfg.Loki))
	for _, cfg := range s.cfg.Loki {
		names = append(names, cfg.Name)
	}

	return names
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
		info.Metadata = metadataValue("url", loki.URL)
		if len(loki.Variants) > 0 {
			info.Metadata = metadataValue("url", loki.Variants[0].URL)
		}
		result = append(result, info)
	}

	return result
}

// BenchmarkoorDatasourceInfo returns detailed benchmarkoor datasource info.
func (s *server) BenchmarkoorDatasourceInfo() []types.DatasourceInfo {
	if len(s.cfg.Benchmarkoor) == 0 {
		return nil
	}

	result := make([]types.DatasourceInfo, 0, len(s.cfg.Benchmarkoor))

	for _, bench := range s.cfg.Benchmarkoor {
		metadata := metadataValue("url", bench.URL)
		if bench.UIURL != "" {
			if metadata == nil {
				metadata = map[string]string{}
			}

			metadata["ui_url"] = bench.UIURL
		}

		result = append(result, types.DatasourceInfo{
			Type:        "benchmarkoor",
			Name:        bench.Name,
			Description: bench.Description,
			Metadata:    metadata,
		})
	}

	return result
}

// ComputeDatasourceInfo returns detailed compute datasource info.
func (s *server) ComputeDatasourceInfo() []types.DatasourceInfo {
	if len(s.cfg.Compute) == 0 {
		return nil
	}

	result := make([]types.DatasourceInfo, 0, len(s.cfg.Compute))

	for _, comp := range s.cfg.Compute {
		// The upstream URL is deliberately omitted: it is the proxy's private
		// backend address and exposing it would leak internal topology to
		// callers and undercut the neutral "compute" alias.
		result = append(result, types.DatasourceInfo{
			Type:        "compute",
			Name:        comp.Name,
			Description: comp.Description,
		})
	}

	return result
}

// EthNodeAvailable returns true if the ethnode handler is configured.
func (s *server) EthNodeAvailable() bool {
	return s.ethNodeHandler != nil
}

// workflowAdvert returns the workflow discovery advert, or nil when the proxy
// is not configured with a workflow engine. The web URL is derived from config
// (web_url else url); the api_token is never advertised.
func (s *server) workflowAdvert() *WorkflowAdvert {
	if s.cfg.Workflow == nil {
		return nil
	}

	return &WorkflowAdvert{
		Enabled: true,
		WebURL:  s.cfg.Workflow.ResolvedWebURL(),
	}
}

// WorkflowInfo reports whether this proxy advertises a workflow engine and the
// web URL for building human links to it, resolved from config.
func (s *server) WorkflowInfo() (enabled bool, webURL string) {
	if s.cfg.Workflow == nil {
		return false, ""
	}

	return true, s.cfg.Workflow.ResolvedWebURL()
}

// EthNodeDatasourceInfo returns the ethnode datasource info when configured.
func (s *server) EthNodeDatasourceInfo() []types.DatasourceInfo {
	return ethNodeDatasourceInfo(s.EthNodeAvailable())
}

// EmbeddingAvailable returns true if either embedding service is configured.
func (s *server) EmbeddingAvailable() bool {
	return s.embeddingService != nil || s.embeddingServiceV2 != nil
}

// EmbeddingModel returns the legacy advertised embedding model. It prefers the
// v1 service and falls through to v2 so v2-only proxies still advertise a model
// to older discovery clients.
func (s *server) EmbeddingModel() string {
	if s.embeddingService != nil {
		return s.embeddingService.Model()
	}

	if s.embeddingServiceV2 != nil {
		return s.embeddingServiceV2.Model()
	}

	return ""
}

func advertisedURLs(listenAddr string) (string, string) {
	port := "18081"
	if _, p, err := net.SplitHostPort(listenAddr); err == nil && p != "" {
		port = p
	}

	url := fmt.Sprintf("http://localhost:%s", port)

	return url, port
}

func listenPortIsEphemeral(listenAddr string) bool {
	_, port, err := net.SplitHostPort(listenAddr)
	return err == nil && port == "0"
}

func urlFromListenerAddr(addr net.Addr, fallback string) string {
	if addr == nil {
		return fallback
	}

	host, port, err := net.SplitHostPort(addr.String())
	if err != nil || port == "" {
		return fallback
	}

	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}

	return "http://" + net.JoinHostPort(host, port)
}
