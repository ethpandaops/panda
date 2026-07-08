package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/internal/version"
	"github.com/ethpandaops/panda/pkg/attribution"
	"github.com/ethpandaops/panda/pkg/auth/token"
	"github.com/ethpandaops/panda/pkg/proxy/handlers"
	"github.com/ethpandaops/panda/pkg/types"
)

// Client connects to a proxy server and provides datasource discovery plus
// proxy-scoped bearer tokens for server-to-proxy calls.
type Client interface {
	Service

	// Discover fetches datasource information from the proxy.
	Discover(ctx context.Context) error

	// EnsureAuthenticated checks if the user has valid credentials.
	EnsureAuthenticated(ctx context.Context) error
}

// ClientConfig configures the proxy client.
type ClientConfig struct {
	// Name is the configured proxy identifier used to tag datasource ownership.
	Name string

	// URL is the base URL of the proxy server (e.g., http://localhost:18081).
	URL string

	// IssuerURL is the OAuth issuer URL for proxy authentication.
	// If empty, URL is used and the client will only work against auth.mode=none proxies.
	IssuerURL string

	// ClientID is the OAuth client ID for authentication.
	ClientID string

	// AuthMode selects the proxy auth flow. Empty/"oauth"/"oidc" use the
	// interactive flows backed by the on-disk credential store;
	// "client_credentials" mints access tokens on demand with Username and
	// Password (Authentik service-account form) and keeps them in memory only.
	AuthMode string

	// Username is the service-account username for AuthMode "client_credentials".
	Username string

	// Password is the service-account app password for AuthMode "client_credentials".
	Password string

	// Resource is the OAuth protected resource to request tokens for.
	// Leave empty for standard OIDC providers that do not use RFC 8707 resource parameters.
	Resource string

	// Scopes are the OAuth scopes to request; empty means the auth client's defaults.
	Scopes []string

	// RefreshTokenTTL is the expected lifetime of the refresh token.
	// When set, the credential store will refresh at 50% of this duration
	// to keep the refresh token alive via provider rotation.
	RefreshTokenTTL time.Duration

	// DiscoveryInterval is how often to refresh datasource info (default: 60 seconds).
	// Set to 0 to disable background refresh.
	DiscoveryInterval time.Duration

	// HTTPTimeout is the timeout for HTTP requests (default: 30 seconds).
	HTTPTimeout time.Duration

	// OnDiscover is invoked after every successful Discover (initial and background).
	// It runs synchronously on the discovery goroutine — keep work short and panic-free.
	// Typical use: re-initialize ProxyDiscoverable modules so newly added datasources
	// surface without a server restart.
	OnDiscover func()
}

// ApplyDefaults sets default values for the client config.
func (c *ClientConfig) ApplyDefaults() {
	if c.Name == "" {
		c.Name = "primary"
	}

	if c.DiscoveryInterval == 0 {
		c.DiscoveryInterval = 60 * time.Second
	}

	if c.HTTPTimeout == 0 {
		c.HTTPTimeout = 30 * time.Second
	}
}

// proxyClient implements Client for connecting to a proxy server.
type proxyClient struct {
	log             logrus.FieldLogger
	cfg             ClientConfig
	httpClient      *http.Client
	queryHTTPClient *http.Client

	// tokenSource yields access tokens for proxy requests regardless of grant
	// type (interactive refresh-token or client_credentials). It is nil when
	// the proxy needs no auth.
	tokenSource token.Source

	mu          sync.RWMutex
	datasources *DatasourcesResponse
	stopCh      chan struct{}
	stopped     bool

	// discovered reports whether at least one datasource discovery has
	// succeeded. It gates server readiness: until the first successful
	// discovery the server has no datasources to serve.
	discovered atomic.Bool

	// authPending reports whether discovery is blocked waiting for the user to
	// authenticate. In that state the server is up and cannot do better until
	// the user runs `panda auth login`, so it counts as ready — otherwise a
	// yet-to-auth user would wedge readiness at 503 forever.
	authPending atomic.Bool
}

// AuthModeClientCredentials is the ClientConfig.AuthMode value for the
// OAuth2 client_credentials grant (Authentik service-account form).
const AuthModeClientCredentials = "client_credentials"

var ErrAuthenticationRequired = errors.New("proxy authentication required")

// Compile-time interface checks.
var (
	_ Client               = (*proxyClient)(nil)
	_ Service              = (*proxyClient)(nil)
	_ WorkflowInfoProvider = (*proxyClient)(nil)
)

// NewClient creates a new proxy client.
func NewClient(log logrus.FieldLogger, cfg ClientConfig) Client {
	cfg.ApplyDefaults()
	transport := &version.Transport{}

	c := &proxyClient{
		log: log.WithField("component", "proxy-client"),
		cfg: cfg,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   cfg.HTTPTimeout,
		},
		queryHTTPClient: &http.Client{Transport: transport},
		datasources:     &DatasourcesResponse{},
		stopCh:          make(chan struct{}),
	}

	// The proxy owns only "what is my issuer" (its own URL is the issuer in
	// embedded mode); the auth package owns everything else — grant choice,
	// OAuth client, and credential store. The proxy treats the result as an
	// opaque token provider, nil when no auth is configured.
	issuerURL := strings.TrimRight(cfg.IssuerURL, "/")
	if issuerURL == "" {
		issuerURL = strings.TrimRight(cfg.URL, "/")
	}

	c.tokenSource = token.NewSource(log, token.Config{
		IssuerURL:       issuerURL,
		ClientID:        cfg.ClientID,
		Resource:        cfg.Resource,
		Scopes:          cfg.Scopes,
		Username:        cfg.Username,
		Password:        cfg.Password,
		Mode:            cfg.AuthMode,
		RefreshTokenTTL: cfg.RefreshTokenTTL,
		MintTimeout:     cfg.HTTPTimeout,
	})

	return c
}

// Start starts the client and performs initial discovery.
func (c *proxyClient) Start(ctx context.Context) error {
	c.log.WithField("url", c.cfg.URL).Info("Starting proxy client")

	// Perform initial discovery.
	if err := c.Discover(ctx); err != nil {
		if errors.Is(err, ErrAuthenticationRequired) {
			c.log.WithError(err).Warn("Proxy discovery skipped until authentication is configured")
		} else {
			c.log.WithError(err).Warn("Initial proxy discovery failed; continuing with background refresh")
		}
	}

	// Start background refresh if configured.
	if c.cfg.DiscoveryInterval > 0 {
		go c.backgroundRefresh()
	}

	return nil
}

// Stop stops the client.
func (c *proxyClient) Stop(_ context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.stopped {
		return nil
	}

	c.stopped = true
	close(c.stopCh)

	c.log.Info("Proxy client stopped")

	return nil
}

// URL returns the proxy URL.
func (c *proxyClient) URL() string {
	return c.cfg.URL
}

func (c *proxyClient) RegisterToken() string {
	if c.tokenSource == nil {
		return NoAuthToken
	}

	tok, err := c.tokenSource.Token(context.Background())
	if err != nil {
		c.log.WithError(err).Debug("Proxy access token is not available")

		return ""
	}

	return tok
}

// Invalidate drops the cached access token so the next request obtains a fresh
// one. Callers use it on a 401/403 before retrying.
func (c *proxyClient) Invalidate() {
	if c.tokenSource != nil {
		c.tokenSource.Invalidate()
	}
}

// accessToken returns the bearer token for a proxy request: an empty string
// when the proxy needs no auth, the token when available, or an error wrapping
// ErrAuthenticationRequired when credentials are missing or unusable.
func (c *proxyClient) accessToken(ctx context.Context) (string, error) {
	if c.tokenSource == nil {
		return "", nil
	}

	tok, err := c.tokenSource.Token(ctx)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrAuthenticationRequired, err)
	}

	return tok, nil
}

func (c *proxyClient) RevokeToken() {
	// No-op: tokens are managed by the proxy control plane.
}

func namesFromInfo(infos []types.DatasourceInfo) []string {
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

func namesToInfo(kind string, names []string, proxyName string) []types.DatasourceInfo {
	if len(names) == 0 {
		return nil
	}

	infos := make([]types.DatasourceInfo, 0, len(names))
	for _, name := range names {
		if name == "" {
			continue
		}
		infos = append(infos, types.DatasourceInfo{
			Type:      kind,
			Name:      name,
			ProxyName: proxyName,
		})
	}

	return infos
}

func normalizeInfo(kind string, infos []types.DatasourceInfo, proxyName string) []types.DatasourceInfo {
	if len(infos) == 0 {
		return nil
	}

	result := make([]types.DatasourceInfo, 0, len(infos))
	for _, info := range infos {
		if info.Name == "" {
			continue
		}
		if info.Type == "" {
			info.Type = kind
		}
		if info.ProxyName == "" {
			info.ProxyName = proxyName
		}
		result = append(result, info)
	}

	return result
}

// ClickHouseDatasources returns the discovered ClickHouse datasource names.
func (c *proxyClient) ClickHouseDatasources() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return datasourceNames(c.datasources.ClickHouseInfo)
}

// ClickHouseDatasourceInfo returns detailed ClickHouse datasource info.
func (c *proxyClient) ClickHouseDatasourceInfo() []types.DatasourceInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return normalizeInfo("clickhouse", c.datasources.ClickHouseInfo, c.cfg.Name)
}

// ClickHouseQuery runs a ClickHouse SQL query against the named datasource by
// POSTing to the proxy's /clickhouse/ route with a proxy-scoped bearer token.
func (c *proxyClient) ClickHouseQuery(ctx context.Context, datasource, sql string, params url.Values) ([]byte, error) {
	if datasource == "" {
		return nil, fmt.Errorf("datasource name is required")
	}

	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.cfg.HTTPTimeout)
		defer cancel()
	}

	baseURL := strings.TrimRight(c.cfg.URL, "/")
	if baseURL == "" {
		return nil, fmt.Errorf("proxy URL is empty")
	}

	requestURL := baseURL + "/clickhouse/"
	if encoded := params.Encode(); encoded != "" {
		requestURL += "?" + encoded
	}

	send := func() (int, []byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, strings.NewReader(sql))
		if err != nil {
			return 0, nil, fmt.Errorf("creating request: %w", err)
		}

		req.Header.Set(handlers.DatasourceHeader, datasource)
		req.Header.Set("Content-Type", "text/plain")

		if v := attribution.FromContext(ctx); v != "" {
			req.Header.Set(attribution.Header, v)
		}

		if token := c.RegisterToken(); token != "" && token != NoAuthToken {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		resp, err := c.queryHTTPClient.Do(req)
		if err != nil {
			return 0, nil, fmt.Errorf("executing query: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return resp.StatusCode, nil, fmt.Errorf("reading response: %w", err)
		}

		return resp.StatusCode, body, nil
	}

	status, body, err := send()
	if err != nil {
		return nil, err
	}

	// One invalidate-and-retry on auth rejection covers a token revoked
	// server-side before the local refresh buffer kicks in.
	if isAuthRejection(status) && c.tokenSource != nil {
		c.log.Debug("Proxy rejected query token; invalidating and retrying")
		c.tokenSource.Invalidate()

		if status, body, err = send(); err != nil {
			return nil, err
		}
	}

	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("query failed (%d): %s", status, strings.TrimSpace(string(body)))
	}

	return body, nil
}

// isAuthRejection reports whether status is a proxy auth rejection that a token
// refresh-and-retry can recover from.
func isAuthRejection(status int) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden
}

// PrometheusDatasources returns the discovered Prometheus datasource names.
func (c *proxyClient) PrometheusDatasources() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return datasourceNames(c.datasources.PrometheusInfo)
}

// PrometheusDatasourceInfo returns detailed Prometheus datasource info.
func (c *proxyClient) PrometheusDatasourceInfo() []types.DatasourceInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return normalizeInfo("prometheus", c.datasources.PrometheusInfo, c.cfg.Name)
}

// LokiDatasources returns the discovered Loki datasource names.
func (c *proxyClient) LokiDatasources() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return datasourceNames(c.datasources.LokiInfo)
}

// LokiDatasourceInfo returns detailed Loki datasource info.
func (c *proxyClient) LokiDatasourceInfo() []types.DatasourceInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return normalizeInfo("loki", c.datasources.LokiInfo, c.cfg.Name)
}

// BenchmarkoorDatasourceInfo returns detailed benchmarkoor datasource info.
func (c *proxyClient) BenchmarkoorDatasourceInfo() []types.DatasourceInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return normalizeInfo("benchmarkoor", c.datasources.BenchmarkoorInfo, c.cfg.Name)
}

func (c *proxyClient) ComputeDatasourceInfo() []types.DatasourceInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return normalizeInfo("compute", c.datasources.ComputeInfo, c.cfg.Name)
}

// EthNodeAvailable returns true if the proxy has ethnode credentials configured.
func (c *proxyClient) EthNodeAvailable() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.datasources.EthNodeAvailable
}

// EthNodeDatasourceInfo returns the ethnode datasource info when configured.
func (c *proxyClient) EthNodeDatasourceInfo() []types.DatasourceInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return ethNodeDatasourceInfo(c.datasources.EthNodeAvailable)
}

// EmbeddingAvailable returns true if the proxy has embedding configured.
func (c *proxyClient) EmbeddingAvailable() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.datasources.EmbeddingAvailable
}

// EmbeddingModel returns the configured legacy embedding model name.
func (c *proxyClient) EmbeddingModel() string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.datasources.EmbeddingModel
}

// WorkflowInfo reports whether the proxy advertises a workflow engine and its
// web URL, read from the cached datasource discovery.
func (c *proxyClient) WorkflowInfo() (enabled bool, webURL string) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.datasources.Workflow == nil {
		return false, ""
	}

	return c.datasources.Workflow.Enabled, c.datasources.Workflow.WebURL
}

// Discover fetches datasource information from the proxy's /datasources endpoint.
// A 401/403 invalidates the cached token and the request is retried once with a
// fresh one, covering proxy-side revocation before the local expiry buffer
// kicks in. This is independent of the auth grant in use.
func (c *proxyClient) Discover(ctx context.Context) error {
	err := c.discoverOnce(ctx)
	if err != nil && errors.Is(err, ErrAuthenticationRequired) && c.tokenSource != nil {
		c.log.WithError(err).Debug("Proxy rejected token; invalidating and retrying")
		c.tokenSource.Invalidate()

		err = c.discoverOnce(ctx)
	}

	switch {
	case err == nil:
		c.discovered.Store(true)
		c.authPending.Store(false)
	case errors.Is(err, ErrAuthenticationRequired):
		// The server is up but cannot discover until the user authenticates.
		// Treat this as ready so the init/start wait does not block on a state
		// only the user can resolve.
		c.authPending.Store(true)
	}

	return err
}

// Ready reports whether the client has completed at least one successful
// datasource discovery, or is blocked waiting for the user to authenticate
// (in which case the server is up and can do no better unattended).
func (c *proxyClient) Ready() bool {
	return c.discovered.Load() || c.authPending.Load()
}

// discoverOnce performs a single /datasources fetch.
func (c *proxyClient) discoverOnce(ctx context.Context) error {
	url := fmt.Sprintf("%s/datasources", c.cfg.URL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	tok, err := c.accessToken(ctx)
	if err != nil {
		return err
	}

	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetching datasources: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)

		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return fmt.Errorf("%w: %s", ErrAuthenticationRequired, strings.TrimSpace(string(body)))
		}

		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var datasources DatasourcesResponse
	if err := json.NewDecoder(resp.Body).Decode(&datasources); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	c.mu.Lock()
	c.datasources = &datasources
	c.mu.Unlock()

	if c.cfg.OnDiscover != nil {
		c.cfg.OnDiscover()
	}

	c.log.WithFields(logrus.Fields{
		"clickhouse": len(datasources.ClickHouseInfo),
		"prometheus": len(datasources.PrometheusInfo),
		"loki":       len(datasources.LokiInfo),
	}).Debug("Discovered datasources from proxy")

	return nil
}

// EnsureAuthenticated checks if the user has valid credentials.
func (c *proxyClient) EnsureAuthenticated(ctx context.Context) error {
	if c.tokenSource == nil {
		// No auth required (e.g., local dev mode).
		return nil
	}

	if _, err := c.tokenSource.Token(ctx); err != nil {
		return fmt.Errorf(
			"not authenticated to proxy. Run `panda auth login` first: %w",
			err,
		)
	}

	return nil
}

// backgroundRefresh periodically refreshes datasource information.
func (c *proxyClient) backgroundRefresh() {
	ticker := time.NewTicker(c.cfg.DiscoveryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopCh:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), c.cfg.HTTPTimeout)

			if err := c.Discover(ctx); err != nil {
				c.log.WithError(err).Warn("Background datasource refresh failed")
			}

			cancel()
		}
	}
}
