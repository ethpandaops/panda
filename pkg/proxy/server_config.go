package proxy

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ethpandaops/panda/pkg/auth"
	"github.com/ethpandaops/panda/pkg/configpath"
	"github.com/ethpandaops/panda/pkg/proxy/handlers"
	"github.com/ethpandaops/panda/pkg/types"
)

// ServerConfig is the configuration for the proxy server.
// This is the single configuration schema used for both local and K8s deployments.
type ServerConfig struct {
	// Server holds HTTP server configuration.
	Server HTTPServerConfig `yaml:"server"`

	// Auth holds authentication configuration.
	Auth AuthConfig `yaml:"auth"`

	// ClickHouse holds ClickHouse cluster configurations.
	ClickHouse []ClickHouseClusterConfig `yaml:"clickhouse,omitempty"`

	// Prometheus holds Prometheus instance configurations.
	Prometheus []PrometheusInstanceConfig `yaml:"prometheus,omitempty"`

	// Loki holds Loki instance configurations.
	Loki []LokiInstanceConfig `yaml:"loki,omitempty"`

	// EthNode holds Ethereum node API access configuration.
	EthNode *EthNodeInstanceConfig `yaml:"ethnode,omitempty"`

	// Benchmarkoor holds benchmarkoor API instance configurations.
	Benchmarkoor []BenchmarkoorInstanceConfig `yaml:"benchmarkoor,omitempty"`

	// Compute holds compute API instance configurations.
	Compute []ComputeInstanceConfig `yaml:"compute,omitempty"`

	// RateLimiting holds rate limiting configuration.
	RateLimiting RateLimitConfig `yaml:"rate_limiting"`

	// Audit holds audit logging configuration.
	Audit AuditConfig `yaml:"audit"`

	// Metrics holds Prometheus metrics configuration.
	Metrics MetricsConfig `yaml:"metrics"`

	// Embedding holds optional embedding API configuration (v1 route: /embed).
	Embedding *EmbeddingConfig `yaml:"embedding,omitempty"`

	// EmbeddingV2 holds optional configuration for the v2/v3 embedding routes.
	// v3 is a protocol upgrade over the same fixed-dimensional embedding space.
	EmbeddingV2 *EmbeddingConfig `yaml:"embedding_v2,omitempty"`

	// GitHub holds optional GitHub API configuration for triggering workflows.
	GitHub *GitHubAPIConfig `yaml:"github,omitempty"`

	// Workflow holds optional workflow-engine passthrough configuration. Nil
	// disables the /workflow route entirely.
	Workflow *WorkflowConfig `yaml:"workflow,omitempty"`

	// Uploads holds optional R2 (S3-compatible) object-store config backing the
	// /uploads route (`panda upload`).
	Uploads *UploadsConfig `yaml:"uploads,omitempty"`
}

// UploadsConfig configures the R2 bucket that backs the /uploads route.
type UploadsConfig struct {
	Bucket          string `yaml:"bucket"`
	KeyPrefix       string `yaml:"key_prefix,omitempty"`
	PublicBaseURL   string `yaml:"public_base_url"`
	Endpoint        string `yaml:"endpoint"`
	AccessKeyID     string `yaml:"access_key_id"`
	SecretAccessKey string `yaml:"secret_access_key"`
	MaxObjectBytes  int64  `yaml:"max_object_bytes,omitempty"`

	// Team enables `panda upload --team`: objects published under a separate
	// key prefix, served from an Access-protected domain instead of the public
	// one. Both fields must be set together; omit them to disable.
	TeamKeyPrefix string `yaml:"team_key_prefix,omitempty"`
	TeamBaseURL   string `yaml:"team_base_url,omitempty"`

	// RenderHTML serves uploaded text/html inline instead of forcing download.
	// Enable only after the bucket domain has the Cloudflare CSP-sandbox rule
	// (see UploadsConfig.RenderHTML in pkg/proxy/handlers).
	RenderHTML bool `yaml:"render_html,omitempty"`
}

// ToUploadsHandlerConfig maps the uploads block to its handler config, or nil
// when uploads are not configured.
func (c *ServerConfig) ToUploadsHandlerConfig() *handlers.UploadsConfig {
	if c.Uploads == nil {
		return nil
	}

	return &handlers.UploadsConfig{
		Bucket:         c.Uploads.Bucket,
		KeyPrefix:      normalizeKeyPrefix(c.Uploads.KeyPrefix),
		PublicBaseURL:  c.Uploads.PublicBaseURL,
		Endpoint:       c.Uploads.Endpoint,
		AccessKeyID:    c.Uploads.AccessKeyID,
		SecretKey:      c.Uploads.SecretAccessKey,
		MaxObjectBytes: c.Uploads.MaxObjectBytes,
		TeamKeyPrefix:  normalizeKeyPrefix(c.Uploads.TeamKeyPrefix),
		TeamBaseURL:    c.Uploads.TeamBaseURL,
		RenderHTML:     c.Uploads.RenderHTML,
	}
}

// normalizeKeyPrefix ensures a non-empty prefix ends with exactly one slash.
func normalizeKeyPrefix(p string) string {
	if p = strings.TrimSpace(p); p == "" {
		return ""
	}

	return strings.TrimRight(p, "/") + "/"
}

// GitHubAPIConfig holds GitHub API configuration for the proxy.
type GitHubAPIConfig struct {
	// Token is a GitHub personal access token or app token with actions:write permission.
	Token string `yaml:"token"`
}

// Workflow auth modes select how the proxy credentials the upstream workflow
// engine on each request.
const (
	// WorkflowAuthModeToken injects the proxy's configured api_token as the
	// bearer — one generic all-users key. This is the default and the
	// local-testing shape.
	WorkflowAuthModeToken = "token"
	// WorkflowAuthModePassthrough re-attaches the caller's own verified bearer
	// so the engine validates the user's OIDC token directly. It requires the
	// proxy to run in an authenticated mode (oauth/oidc).
	WorkflowAuthModePassthrough = "passthrough"
)

// WorkflowConfig holds the workflow-engine passthrough configuration. The API
// token lives HERE, at the proxy layer, never on the panda server.
type WorkflowConfig struct {
	// URL is the workflow engine API origin. Bare scheme+host[:port]; no
	// path/query/fragment/userinfo.
	URL string `yaml:"url"`
	// AuthMode is "token" (default; inject APIToken) or "passthrough" (forward
	// the caller's own bearer for the engine to validate directly).
	AuthMode string `yaml:"auth_mode,omitempty"`
	// APIToken is required when AuthMode is "token". ${ENV}-substitutable.
	APIToken string `yaml:"api_token,omitempty"`
	// WebURL is the frontend-link origin (may carry a path). Defaults to URL.
	WebURL string `yaml:"web_url,omitempty"`
	// AllowedOrgs restricts /workflow access to members of these GitHub orgs.
	AllowedOrgs []string `yaml:"allowed_orgs,omitempty"`
}

// ResolvedAuthMode returns the effective auth mode, defaulting to "token".
func (w *WorkflowConfig) ResolvedAuthMode() string {
	if strings.TrimSpace(w.AuthMode) == "" {
		return WorkflowAuthModeToken
	}

	return strings.TrimSpace(w.AuthMode)
}

// ResolvedWebURL returns the frontend-link origin: web_url when set, else url,
// with a single trailing slash trimmed. It never returns the api_token.
func (w *WorkflowConfig) ResolvedWebURL() string {
	base := strings.TrimSpace(w.WebURL)
	if base == "" {
		base = strings.TrimSpace(w.URL)
	}

	return strings.TrimRight(base, "/")
}

// HTTPServerConfig holds HTTP server configuration.
type HTTPServerConfig struct {
	// ListenAddr is the address to listen on (default: ":18081").
	ListenAddr string `yaml:"listen_addr,omitempty"`

	// ReadTimeout is the maximum duration for reading the entire request.
	ReadTimeout time.Duration `yaml:"read_timeout,omitempty"`

	// WriteTimeout is the maximum duration before timing out writes of the response.
	WriteTimeout time.Duration `yaml:"write_timeout,omitempty"`

	// IdleTimeout is the maximum amount of time to wait for the next request.
	IdleTimeout time.Duration `yaml:"idle_timeout,omitempty"`
}

// AuthConfig holds authentication configuration for the proxy.
type AuthConfig struct {
	// Mode is the authentication mode.
	Mode AuthMode `yaml:"mode"`

	// IssuerURL is the proxy's own issuer URL (mode 'oauth'), advertised to
	// clients for login discovery.
	IssuerURL string `yaml:"issuer_url,omitempty"`

	// ClientID is the client identifier advertised to clients (mode 'oauth').
	ClientID string `yaml:"client_id,omitempty"`

	// Issuers are the trusted external OIDC issuers (mode 'oidc'). A bearer token
	// is accepted if it verifies against ANY of them, letting the proxy trust
	// e.g. humans on one IdP and CI service accounts on another. The first issuer
	// is advertised to clients for interactive login.
	Issuers []OIDCIssuerConfig `yaml:"issuers,omitempty"`

	// GitHub configures the GitHub OAuth app used for user authentication.
	GitHub *auth.GitHubConfig `yaml:"github,omitempty"`

	// AllowedOrgs restricts access to members of these GitHub orgs.
	AllowedOrgs []string `yaml:"allowed_orgs,omitempty"`

	// Tokens configures proxy-issued bearer tokens.
	Tokens auth.TokensConfig `yaml:"tokens"`

	// AccessTokenTTL is the lifetime of proxy-issued access tokens.
	AccessTokenTTL time.Duration `yaml:"access_token_ttl,omitempty"`

	// RefreshTokenTTL is the lifetime of proxy-issued refresh tokens.
	RefreshTokenTTL time.Duration `yaml:"refresh_token_ttl,omitempty"`

	// SuccessPage customizes the OAuth callback success page shown in the browser.
	SuccessPage *auth.SuccessPageConfig `yaml:"success_page,omitempty"`
}

// DatasourceConfig is the interface every datasource config must satisfy.
// The Authorizer uses this to build access rules generically, ensuring that
// any new datasource type added to the proxy must include authorization support.
type DatasourceConfig interface {
	DatasourceName() string
	DatasourceDescription() string
	DatasourceAllowedOrgs() []string
}

// BaseDatasourceConfig holds fields common to all datasource configurations.
// Embed this in every datasource config struct to get compile-time enforcement
// of authorization support via the DatasourceConfig interface.
type BaseDatasourceConfig struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description,omitempty"`
	AllowedOrgs []string `yaml:"allowed_orgs,omitempty"`
}

// DatasourceName returns the datasource name.
func (b BaseDatasourceConfig) DatasourceName() string { return b.Name }

// DatasourceDescription returns the datasource description.
func (b BaseDatasourceConfig) DatasourceDescription() string { return b.Description }

// DatasourceAllowedOrgs returns the list of GitHub orgs allowed to access this datasource.
func (b BaseDatasourceConfig) DatasourceAllowedOrgs() []string { return b.AllowedOrgs }

// Compile-time interface checks for datasource configs.
var (
	_ DatasourceConfig = ClickHouseClusterConfig{}
	_ DatasourceConfig = PrometheusInstanceConfig{}
	_ DatasourceConfig = LokiInstanceConfig{}
	_ DatasourceConfig = EthNodeInstanceConfig{}
	_ DatasourceConfig = BenchmarkoorInstanceConfig{}
	_ DatasourceConfig = ComputeInstanceConfig{}
)

// ClickHouseClusterConfig holds ClickHouse cluster configuration.
type ClickHouseClusterConfig struct {
	BaseDatasourceConfig `yaml:",inline"`
	Host                 string                           `yaml:"host"`
	Port                 int                              `yaml:"port"`
	Database             string                           `yaml:"database,omitempty"`
	Username             string                           `yaml:"username"`
	Password             string                           `yaml:"password"`
	Secure               bool                             `yaml:"secure"`
	SkipVerify           bool                             `yaml:"skip_verify,omitempty"`
	Timeout              int                              `yaml:"timeout,omitempty"`
	Autodiscover         bool                             `yaml:"autodiscover,omitempty"`
	AutodiscoverInterval time.Duration                    `yaml:"autodiscover_interval,omitempty"`
	Variants             []ClickHouseClusterVariantConfig `yaml:"variants,omitempty"`
	// Contains declares the datasets stored in this cluster. Passed through to
	// discovery verbatim; the proxy never interprets Params or Notes.
	Contains []DatasetBindingConfig `yaml:"contains,omitempty"`
}

// DatasetBindingConfig declares a dataset stored in a datasource. The dataset
// name matches a knowledge pack in the release; Params are opaque placement
// hints interpreted by that pack (e.g. database: default); Notes says what
// distinguishes this copy from the dataset's other copies — universal query
// knowledge belongs in the dataset pack, cluster-wide behavior in the
// datasource description.
type DatasetBindingConfig struct {
	Dataset string            `yaml:"dataset"`
	Params  map[string]string `yaml:"params,omitempty"`
	Notes   string            `yaml:"notes,omitempty"`
}

func (c DatasetBindingConfig) toBinding() types.DatasetBinding {
	return types.DatasetBinding{Dataset: c.Dataset, Params: c.Params, Notes: c.Notes}
}

func bindingsToInfo(bindings []DatasetBindingConfig) []types.DatasetBinding {
	if len(bindings) == 0 {
		return nil
	}

	out := make([]types.DatasetBinding, 0, len(bindings))
	for _, b := range bindings {
		if b.Dataset == "" {
			continue
		}

		out = append(out, b.toBinding())
	}

	return out
}

// ClickHouseClusterVariantConfig holds one selectable ClickHouse backend.
type ClickHouseClusterVariantConfig struct {
	AllowedOrgs []string `yaml:"allowed_orgs,omitempty"`
	Host        string   `yaml:"host"`
	Port        int      `yaml:"port"`
	Database    string   `yaml:"database,omitempty"`
	Username    string   `yaml:"username"`
	Password    string   `yaml:"password"`
	Secure      bool     `yaml:"secure"`
	SkipVerify  bool     `yaml:"skip_verify,omitempty"`
	Timeout     int      `yaml:"timeout,omitempty"`
}

// PrometheusInstanceConfig holds Prometheus instance configuration.
type PrometheusInstanceConfig struct {
	BaseDatasourceConfig `yaml:",inline"`
	URL                  string                            `yaml:"url"`
	Username             string                            `yaml:"username,omitempty"`
	Password             string                            `yaml:"password,omitempty"`
	Variants             []PrometheusInstanceVariantConfig `yaml:"variants,omitempty"`
}

// PrometheusInstanceVariantConfig holds one selectable Prometheus backend.
type PrometheusInstanceVariantConfig struct {
	AllowedOrgs []string `yaml:"allowed_orgs,omitempty"`
	URL         string   `yaml:"url"`
	Username    string   `yaml:"username,omitempty"`
	Password    string   `yaml:"password,omitempty"`
}

// LokiInstanceConfig holds Loki instance configuration.
type LokiInstanceConfig struct {
	BaseDatasourceConfig `yaml:",inline"`
	URL                  string                      `yaml:"url"`
	Username             string                      `yaml:"username,omitempty"`
	Password             string                      `yaml:"password,omitempty"`
	Variants             []LokiInstanceVariantConfig `yaml:"variants,omitempty"`
}

// LokiInstanceVariantConfig holds one selectable Loki backend.
type LokiInstanceVariantConfig struct {
	AllowedOrgs []string `yaml:"allowed_orgs,omitempty"`
	URL         string   `yaml:"url"`
	Username    string   `yaml:"username,omitempty"`
	Password    string   `yaml:"password,omitempty"`
}

// EthNodeInstanceConfig holds Ethereum node API access configuration.
// A single credential pair is used for all beacon and execution node endpoints.
type EthNodeInstanceConfig struct {
	BaseDatasourceConfig `yaml:",inline"`
	Username             string `yaml:"username"`
	Password             string `yaml:"password"`
}

// BenchmarkoorInstanceConfig holds benchmarkoor API instance configuration.
// Benchmarkoor is the execution-client benchmarking service; APIKey is a
// read-only benchmarkoor API key (bmk_...) injected as a Bearer token.
type BenchmarkoorInstanceConfig struct {
	BaseDatasourceConfig `yaml:",inline"`
	URL                  string `yaml:"url"`
	APIKey               string `yaml:"api_key,omitempty"`
	// UIURL is the public benchmarkoor web UI, used to build deep links to
	// runs and suites. The UI host is not derivable from the API URL.
	UIURL string `yaml:"ui_url,omitempty"`
}

// ComputeInstanceConfig holds compute API instance configuration. The compute
// backend manages ephemeral sandboxes and snapshots; it validates the caller's
// OIDC bearer token directly, so the proxy forwards that token unchanged (after
// verifying it and enforcing allowed_orgs) — no service token is injected.
type ComputeInstanceConfig struct {
	BaseDatasourceConfig `yaml:",inline"`
	URL                  string `yaml:"url"`
}

// RateLimitConfig holds rate limiting configuration.
type RateLimitConfig struct {
	// Enabled controls whether rate limiting is active.
	Enabled bool `yaml:"enabled"`

	// RequestsPerMinute is the maximum requests per minute per user.
	RequestsPerMinute int `yaml:"requests_per_minute,omitempty"`

	// BurstSize is the maximum burst size.
	BurstSize int `yaml:"burst_size,omitempty"`
}

// AuditConfig holds audit logging configuration.
type AuditConfig struct {
	// Enabled controls whether audit logging is active.
	Enabled bool `yaml:"enabled"`
	// LogRequestBody captures the upstream request payload (e.g. the ClickHouse
	// SQL or other POST body) in the audit entry. The full body is always
	// forwarded upstream; only the audited copy is truncated to MaxBodyBytes.
	// Defaults to true when unset.
	LogRequestBody *bool `yaml:"log_request_body"`
	// LogResponseBody captures the upstream response payload in the audit entry.
	// Response bodies can be large (full result sets), so this is off by default;
	// the audited copy is truncated to MaxBodyBytes.
	LogResponseBody bool `yaml:"log_response_body"`
	// MaxBodyBytes caps how many bytes of each captured body are stored in the
	// audit entry. Zero falls back to defaultMaxAuditBodyBytes.
	MaxBodyBytes int `yaml:"max_body_bytes"`
}

// EmbeddingConfig holds configuration for the remote embedding API.
type EmbeddingConfig struct {
	// APIKey is the API key for the embedding provider (e.g., OpenRouter).
	APIKey string `yaml:"api_key"`

	// Model is the embedding model name (default: "openai/text-embedding-3-small"
	// for v1, "google/gemini-embedding-2" for v2/v3).
	Model string `yaml:"model,omitempty"`

	// APIURL is the base URL of the embedding API (default: "https://openrouter.ai/api/v1").
	APIURL string `yaml:"api_url,omitempty"`

	// Dimensions requests a fixed output dimensionality (Matryoshka) from the
	// embedding API. Used by embedding_v2; ignored by the v1 embedding block.
	Dimensions int `yaml:"dimensions,omitempty"`

	// Cache holds embedding cache configuration.
	Cache EmbeddingCacheConfig `yaml:"cache"`
}

// EmbeddingCacheConfig holds cache configuration for embeddings.
type EmbeddingCacheConfig struct {
	// Backend is the cache backend: "memory" (default) or "redis".
	Backend string `yaml:"backend,omitempty"`

	// RedisURL is the Redis connection URL (required when backend is "redis").
	RedisURL string `yaml:"redis_url,omitempty"`
}

// MetricsConfig holds Prometheus metrics configuration for the proxy.
type MetricsConfig struct {
	// Enabled controls whether the Prometheus metrics server is active.
	Enabled bool `yaml:"enabled"`

	// ListenAddr is the address to serve the /metrics endpoint on.
	ListenAddr string `yaml:"listen_addr,omitempty"`

	// Port is the port to serve the /metrics endpoint on (default: 9090).
	Port int `yaml:"port,omitempty"`
}

// ApplyDefaults sets default values for the server config.
func (c *ServerConfig) ApplyDefaults() {
	// Server defaults.
	if c.Server.ListenAddr == "" {
		c.Server.ListenAddr = ":18081"
	}

	if c.Server.ReadTimeout == 0 {
		c.Server.ReadTimeout = 30 * time.Second
	}

	if c.Server.WriteTimeout == 0 {
		c.Server.WriteTimeout = 5 * time.Minute
	}

	if c.Server.IdleTimeout == 0 {
		c.Server.IdleTimeout = 60 * time.Second
	}

	// Auth defaults.
	// Default to no auth for local development. Hosted deployments should explicitly set oauth mode.
	if c.Auth.Mode == "" {
		c.Auth.Mode = AuthModeNone
	}

	if c.Auth.AccessTokenTTL == 0 {
		c.Auth.AccessTokenTTL = 1 * time.Hour
	}

	if c.Auth.RefreshTokenTTL == 0 {
		c.Auth.RefreshTokenTTL = 30 * 24 * time.Hour
	}

	// Rate limiting defaults.
	if c.RateLimiting.RequestsPerMinute == 0 {
		c.RateLimiting.RequestsPerMinute = 60
	}

	if c.RateLimiting.BurstSize == 0 {
		c.RateLimiting.BurstSize = 10
	}

	// Audit defaults. Request body logging is on by default; response body
	// logging is opt-in (zero value false).
	if c.Audit.LogRequestBody == nil {
		enabled := true
		c.Audit.LogRequestBody = &enabled
	}

	// Metrics defaults.
	if c.Metrics.Port == 0 {
		c.Metrics.Port = 9090
	}

	if c.Metrics.ListenAddr == "" {
		c.Metrics.ListenAddr = fmt.Sprintf("127.0.0.1:%d", c.Metrics.Port)
	}

	// Embedding defaults.
	if c.Embedding != nil {
		if c.Embedding.Model == "" {
			c.Embedding.Model = "openai/text-embedding-3-small"
		}

		if c.Embedding.APIURL == "" {
			c.Embedding.APIURL = "https://openrouter.ai/api/v1"
		}

		if c.Embedding.Cache.Backend == "" {
			c.Embedding.Cache.Backend = "memory"
		}
	}

	// Embedding v2/v3 defaults.
	if c.EmbeddingV2 != nil {
		if c.EmbeddingV2.Model == "" {
			c.EmbeddingV2.Model = "google/gemini-embedding-2"
		}

		if c.EmbeddingV2.APIURL == "" {
			c.EmbeddingV2.APIURL = "https://openrouter.ai/api/v1"
		}

		if c.EmbeddingV2.Dimensions == 0 {
			c.EmbeddingV2.Dimensions = 1536
		}

		if c.EmbeddingV2.Cache.Backend == "" {
			c.EmbeddingV2.Cache.Backend = "memory"
		}
	}

	// ClickHouse defaults.
	for i := range c.ClickHouse {
		if len(c.ClickHouse[i].Variants) > 0 {
			for j := range c.ClickHouse[i].Variants {
				if c.ClickHouse[i].Variants[j].Port == 0 {
					c.ClickHouse[i].Variants[j].Port = defaultClickHousePort(c.ClickHouse[i].Variants[j].Secure)
				}
			}

			continue
		}

		if c.ClickHouse[i].Port == 0 {
			c.ClickHouse[i].Port = defaultClickHousePort(c.ClickHouse[i].Secure)
		}

		if c.ClickHouse[i].Autodiscover && c.ClickHouse[i].AutodiscoverInterval == 0 {
			c.ClickHouse[i].AutodiscoverInterval = defaultAutodiscoverInterval
		}
	}
}

// Validate validates the server config.
func (c *ServerConfig) Validate() error {
	if c.Auth.Mode == AuthModeOAuth {
		if c.Auth.GitHub == nil {
			return fmt.Errorf("auth.github is required when auth.mode is 'oauth'")
		}

		if c.Auth.GitHub.ClientID == "" {
			return fmt.Errorf("auth.github.client_id is required")
		}

		if c.Auth.GitHub.ClientSecret == "" {
			return fmt.Errorf("auth.github.client_secret is required")
		}

		if c.Auth.Tokens.SecretKey == "" {
			return fmt.Errorf("auth.tokens.secret_key is required")
		}

		if strings.TrimSpace(c.Auth.IssuerURL) == "" {
			return fmt.Errorf("auth.issuer_url is required")
		}
	}

	if c.Auth.Mode == AuthModeOIDC {
		if len(c.Auth.Issuers) == 0 {
			return fmt.Errorf("auth.issuers must contain at least one issuer when auth.mode is 'oidc'")
		}

		for i, issuer := range c.Auth.Issuers {
			if strings.TrimSpace(issuer.IssuerURL) == "" {
				return fmt.Errorf("auth.issuers[%d].issuer_url is required", i)
			}

			if strings.TrimSpace(issuer.ClientID) == "" {
				return fmt.Errorf("auth.issuers[%d].client_id is required", i)
			}
		}
	}

	// Validate embedding config.
	if c.Embedding != nil {
		if c.Embedding.APIKey == "" {
			return fmt.Errorf("embedding.api_key is required when embedding is configured")
		}

		if c.Embedding.Cache.Backend == "redis" && c.Embedding.Cache.RedisURL == "" {
			return fmt.Errorf("embedding.cache.redis_url is required when cache backend is 'redis'")
		}
	}

	// Validate embedding v2/v3 config.
	if c.EmbeddingV2 != nil {
		if c.EmbeddingV2.APIKey == "" {
			return fmt.Errorf("embedding_v2.api_key is required when embedding_v2 is configured")
		}

		// Dimensions is embedding-space identity; a non-positive value would
		// start cleanly, advertise a bad space, then fail every embed request.
		if c.EmbeddingV2.Dimensions < 1 {
			return fmt.Errorf("embedding_v2.dimensions must be positive, got %d", c.EmbeddingV2.Dimensions)
		}

		if c.EmbeddingV2.Cache.Backend == "redis" && c.EmbeddingV2.Cache.RedisURL == "" {
			return fmt.Errorf("embedding_v2.cache.redis_url is required when cache backend is 'redis'")
		}
	}

	// Validate the proxy serves something: at least one datasource, or the
	// workflow engine (a proxy dedicated to engine passthrough is valid).
	if len(c.ClickHouse) == 0 && len(c.Prometheus) == 0 && len(c.Loki) == 0 && c.EthNode == nil && len(c.Benchmarkoor) == 0 && len(c.Compute) == 0 && c.Workflow == nil {
		return fmt.Errorf("at least one datasource (clickhouse, prometheus, loki, ethnode, benchmarkoor, or compute) or the workflow engine must be configured")
	}

	// Validate ClickHouse configs.
	for i, ch := range c.ClickHouse {
		if ch.Name == "" {
			return fmt.Errorf("clickhouse[%d].name is required", i)
		}

		for j, b := range ch.Contains {
			if b.Dataset == "" {
				return fmt.Errorf("clickhouse[%d].contains[%d].dataset is required", i, j)
			}
		}

		if ch.AutodiscoverInterval < 0 {
			return fmt.Errorf("clickhouse[%d].autodiscover_interval cannot be negative", i)
		}

		if len(ch.Variants) > 0 {
			if ch.Autodiscover {
				return fmt.Errorf("clickhouse[%d] %q cannot use autodiscover with variants", i, ch.Name)
			}

			if len(ch.AllowedOrgs) > 0 {
				return fmt.Errorf("clickhouse[%d] %q cannot set top-level allowed_orgs with variants", i, ch.Name)
			}

			if ch.Host != "" || ch.Port != 0 || ch.Database != "" ||
				ch.Username != "" || ch.Password != "" || ch.Secure || ch.SkipVerify || ch.Timeout != 0 {
				return fmt.Errorf("clickhouse[%d] %q cannot mix top-level backend fields with variants", i, ch.Name)
			}

			for j, variant := range ch.Variants {
				if variant.Host == "" {
					return fmt.Errorf("clickhouse[%d].variants[%d].host is required", i, j)
				}
			}

			continue
		}

		if ch.Host == "" {
			return fmt.Errorf("clickhouse[%d].host is required", i)
		}

		if ch.Autodiscover && strings.TrimSpace(ch.Database) == "" {
			return fmt.Errorf("clickhouse[%d].database is required when autodiscover is enabled", i)
		}
	}

	// Validate Prometheus configs.
	for i, prom := range c.Prometheus {
		if prom.Name == "" {
			return fmt.Errorf("prometheus[%d].name is required", i)
		}

		if len(prom.Variants) > 0 {
			if len(prom.AllowedOrgs) > 0 {
				return fmt.Errorf("prometheus[%d] %q cannot set top-level allowed_orgs with variants", i, prom.Name)
			}

			if prom.URL != "" || prom.Username != "" || prom.Password != "" {
				return fmt.Errorf("prometheus[%d] %q cannot mix top-level backend fields with variants", i, prom.Name)
			}

			for j, variant := range prom.Variants {
				if variant.URL == "" {
					return fmt.Errorf("prometheus[%d].variants[%d].url is required", i, j)
				}
			}

			continue
		}

		if prom.URL == "" {
			return fmt.Errorf("prometheus[%d].url is required", i)
		}
	}

	// Validate benchmarkoor configs.
	for i, bench := range c.Benchmarkoor {
		if bench.Name == "" {
			return fmt.Errorf("benchmarkoor[%d].name is required", i)
		}

		if bench.URL == "" {
			return fmt.Errorf("benchmarkoor[%d].url is required", i)
		}
	}

	// Validate compute configs.
	for i, comp := range c.Compute {
		if comp.Name == "" {
			return fmt.Errorf("compute[%d].name is required", i)
		}

		if comp.URL == "" {
			return fmt.Errorf("compute[%d].url is required", i)
		}
	}

	// Validate Loki configs.
	for i, loki := range c.Loki {
		if loki.Name == "" {
			return fmt.Errorf("loki[%d].name is required", i)
		}

		if len(loki.Variants) > 0 {
			if len(loki.AllowedOrgs) > 0 {
				return fmt.Errorf("loki[%d] %q cannot set top-level allowed_orgs with variants", i, loki.Name)
			}

			if loki.URL != "" || loki.Username != "" || loki.Password != "" {
				return fmt.Errorf("loki[%d] %q cannot mix top-level backend fields with variants", i, loki.Name)
			}

			for j, variant := range loki.Variants {
				if variant.URL == "" {
					return fmt.Errorf("loki[%d].variants[%d].url is required", i, j)
				}
			}

			continue
		}

		if loki.URL == "" {
			return fmt.Errorf("loki[%d].url is required", i)
		}
	}

	if err := c.validateWorkflow(); err != nil {
		return err
	}

	return nil
}

// validateWorkflow validates the optional workflow block. It asserts only when
// workflow is enabled (non-nil) so existing no-workflow configs keep validating.
func (c *ServerConfig) validateWorkflow() error {
	if c.Workflow == nil {
		return nil
	}

	parsed, err := url.Parse(strings.TrimSpace(c.Workflow.URL))
	if err != nil {
		return fmt.Errorf("workflow.url is invalid: %w", err)
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("workflow.url must use an http or https scheme, got %q", parsed.Scheme)
	}

	if parsed.Host == "" {
		return errors.New("workflow.url must include a host")
	}

	// The passthrough injects its own bearer, so embedded credentials would be a
	// silent trap — reject them rather than ignore them.
	if parsed.User != nil {
		return errors.New("workflow.url must not include userinfo")
	}

	// url.Port() returns only an all-digit port (or ""), so validate the range.
	if portStr := parsed.Port(); portStr != "" {
		port, portErr := strconv.Atoi(portStr)
		if portErr != nil || port < 1 || port > 65535 {
			return fmt.Errorf("workflow.url has an invalid port %q, must be 1..65535", portStr)
		}
	}

	// A bare host or a single trailing slash is path-less; anything else would
	// corrupt the /api/v1/<rest> join the passthrough builds.
	if parsed.Path != "" && parsed.Path != "/" {
		return fmt.Errorf("workflow.url must not include a path, got %q", parsed.Path)
	}

	if parsed.RawQuery != "" {
		return fmt.Errorf("workflow.url must not include a query, got %q", parsed.RawQuery)
	}

	if parsed.Fragment != "" {
		return fmt.Errorf("workflow.url must not include a fragment, got %q", parsed.Fragment)
	}

	if err := validateWorkflowWebURL(c.Workflow.WebURL); err != nil {
		return err
	}

	switch c.Workflow.ResolvedAuthMode() {
	case WorkflowAuthModeToken:
		if strings.TrimSpace(c.Workflow.APIToken) == "" {
			return errors.New("workflow.api_token is required when workflow.auth_mode is token")
		}
	case WorkflowAuthModePassthrough:
		if strings.TrimSpace(c.Workflow.APIToken) != "" {
			return errors.New("workflow.api_token must be empty when workflow.auth_mode is passthrough")
		}

		if c.Auth.Mode != AuthModeOAuth && c.Auth.Mode != AuthModeOIDC {
			return fmt.Errorf(
				"workflow.auth_mode passthrough requires proxy auth.mode oauth or oidc, got %q",
				c.Auth.Mode,
			)
		}
	default:
		return fmt.Errorf(
			"workflow.auth_mode must be one of {%s, %s}, got %q",
			WorkflowAuthModeToken, WorkflowAuthModePassthrough, c.Workflow.AuthMode,
		)
	}

	return nil
}

// validateWorkflowWebURL validates the optional frontend-link origin. Unlike
// url it may carry a path (a UI mounted under a subpath), but it is a link
// prefix for humans, so scheme+host are still required and query/fragment still
// make no sense.
func validateWorkflowWebURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("workflow.web_url is invalid: %w", err)
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("workflow.web_url must use an http or https scheme, got %q", parsed.Scheme)
	}

	if parsed.Host == "" {
		return errors.New("workflow.web_url must include a host")
	}

	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("workflow.web_url must not include a query or fragment")
	}

	return nil
}

// ToBenchmarkoorHandlerConfigs converts the benchmarkoor instance configs to
// handler configs.
func (c *ServerConfig) ToBenchmarkoorHandlerConfigs() []handlers.BenchmarkoorConfig {
	if len(c.Benchmarkoor) == 0 {
		return nil
	}

	configs := make([]handlers.BenchmarkoorConfig, 0, len(c.Benchmarkoor))
	for _, bench := range c.Benchmarkoor {
		configs = append(configs, handlers.BenchmarkoorConfig{
			Name:        bench.Name,
			Description: bench.Description,
			URL:         bench.URL,
			APIKey:      bench.APIKey,
		})
	}

	return configs
}

// ToComputeHandlerConfigs converts the compute instance configs to handler
// configs.
func (c *ServerConfig) ToComputeHandlerConfigs() []handlers.ComputeConfig {
	if len(c.Compute) == 0 {
		return nil
	}

	configs := make([]handlers.ComputeConfig, 0, len(c.Compute))
	for _, comp := range c.Compute {
		configs = append(configs, handlers.ComputeConfig{
			Name:        comp.Name,
			Description: comp.Description,
			URL:         comp.URL,
		})
	}

	return configs
}

// ToHandlerConfigs converts the server config to handler configs.
func (c *ServerConfig) ToHandlerConfigs() ([]handlers.ClickHouseConfig, []handlers.PrometheusConfig, []handlers.LokiConfig, *handlers.EthNodeConfig) {
	// Convert ClickHouse configs.
	chConfigs := make([]handlers.ClickHouseConfig, 0, len(c.ClickHouse))
	for _, ch := range c.ClickHouse {
		if ch.Autodiscover {
			continue
		}

		if len(ch.Variants) > 0 {
			for i, variant := range ch.Variants {
				chConfigs = append(chConfigs, handlers.ClickHouseConfig{
					Name:        ch.Name,
					RouteName:   datasourceVariantRouteName(ch.Name, i),
					Description: ch.Description,
					Host:        variant.Host,
					Port:        variant.Port,
					Database:    variant.Database,
					Username:    variant.Username,
					Password:    variant.Password,
					Secure:      variant.Secure,
					SkipVerify:  variant.SkipVerify,
					Timeout:     variant.Timeout,
				})
			}

			continue
		}

		chConfigs = append(chConfigs, handlers.ClickHouseConfig{
			Name:        ch.Name,
			Description: ch.Description,
			Host:        ch.Host,
			Port:        ch.Port,
			Database:    ch.Database,
			Username:    ch.Username,
			Password:    ch.Password,
			Secure:      ch.Secure,
			SkipVerify:  ch.SkipVerify,
			Timeout:     ch.Timeout,
		})
	}

	// Convert Prometheus configs.
	promConfigs := make([]handlers.PrometheusConfig, 0, len(c.Prometheus))
	for _, prom := range c.Prometheus {
		if len(prom.Variants) > 0 {
			for i, variant := range prom.Variants {
				promConfigs = append(promConfigs, handlers.PrometheusConfig{
					Name:        prom.Name,
					RouteName:   datasourceVariantRouteName(prom.Name, i),
					Description: prom.Description,
					URL:         variant.URL,
					Username:    variant.Username,
					Password:    variant.Password,
				})
			}

			continue
		}

		promConfigs = append(promConfigs, handlers.PrometheusConfig{
			Name:        prom.Name,
			Description: prom.Description,
			URL:         prom.URL,
			Username:    prom.Username,
			Password:    prom.Password,
		})
	}

	// Convert Loki configs.
	lokiConfigs := make([]handlers.LokiConfig, 0, len(c.Loki))
	for _, loki := range c.Loki {
		if len(loki.Variants) > 0 {
			for i, variant := range loki.Variants {
				lokiConfigs = append(lokiConfigs, handlers.LokiConfig{
					Name:        loki.Name,
					RouteName:   datasourceVariantRouteName(loki.Name, i),
					Description: loki.Description,
					URL:         variant.URL,
					Username:    variant.Username,
					Password:    variant.Password,
				})
			}

			continue
		}

		lokiConfigs = append(lokiConfigs, handlers.LokiConfig{
			Name:        loki.Name,
			Description: loki.Description,
			URL:         loki.URL,
			Username:    loki.Username,
			Password:    loki.Password,
		})
	}

	// Convert EthNode config.
	var ethNodeConfig *handlers.EthNodeConfig
	if c.EthNode != nil && c.EthNode.Username != "" {
		ethNodeConfig = &handlers.EthNodeConfig{
			Username: c.EthNode.Username,
			Password: c.EthNode.Password,
		}
	}

	return chConfigs, promConfigs, lokiConfigs, ethNodeConfig
}

func defaultClickHousePort(secure bool) int {
	if secure {
		return 8443
	}

	return 8123
}

func datasourceVariantRouteName(name string, index int) string {
	return fmt.Sprintf("%s\x00variant\x00%d", name, index)
}

func metadataValue(key, value string) map[string]string {
	if value == "" {
		return nil
	}

	return map[string]string{key: value}
}

// envVarWithDefaultPattern matches ${VAR_NAME:-default} patterns.
var envVarWithDefaultPattern = regexp.MustCompile(`\$\{([^}:]+)(?::-([^}]*))?\}`)

// LoadServerConfig loads a proxy server config from a YAML file.
func LoadServerConfig(path string) (*ServerConfig, error) {
	resolvedPath, err := configpath.ResolveProxyConfigPath(path, "")
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("reading config file %s: %w", resolvedPath, err)
	}

	// Substitute environment variables.
	substituted, err := substituteEnvVars(string(data))
	if err != nil {
		return nil, fmt.Errorf("substituting env vars: %w", err)
	}

	var cfg ServerConfig
	if err := yaml.Unmarshal([]byte(substituted), &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	cfg.ApplyDefaults()

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	return &cfg, nil
}

// substituteEnvVars replaces ${VAR_NAME} and ${VAR_NAME:-default} patterns with environment variable values.
// Lines that are comments (starting with #) are skipped.
// Missing environment variables without defaults are replaced with empty strings (lenient mode).
func substituteEnvVars(content string) (string, error) {
	lines := strings.Split(content, "\n")

	for i, line := range lines {
		// Skip lines that are YAML comments.
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}

		lines[i] = envVarWithDefaultPattern.ReplaceAllStringFunc(line, func(match string) string {
			parts := envVarWithDefaultPattern.FindStringSubmatch(match)
			varName := parts[1]
			defaultVal := ""
			if len(parts) > 2 {
				defaultVal = parts[2]
			}

			value := os.Getenv(varName)
			if value == "" {
				return defaultVal // Use default or empty string
			}

			return value
		})
	}

	return strings.Join(lines, "\n"), nil
}
