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
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/internal/version"
	"github.com/ethpandaops/panda/pkg/attribution"
	"github.com/ethpandaops/panda/pkg/auth/client"
	"github.com/ethpandaops/panda/pkg/auth/store"
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
	authClient      client.Client
	credStore       store.Store

	mu          sync.RWMutex
	datasources *DatasourcesResponse
	stopCh      chan struct{}
	stopped     bool

	// ccMu guards ccTokens, the in-memory client_credentials token cache.
	// Tokens minted via client_credentials are never written to disk.
	ccMu     sync.Mutex
	ccTokens *client.Tokens
}

// AuthModeClientCredentials is the ClientConfig.AuthMode value for the
// OAuth2 client_credentials grant (Authentik service-account form).
const AuthModeClientCredentials = "client_credentials"

// clientCredentialsRefreshBuffer is how long before expiry a cached
// client_credentials access token is re-minted.
const clientCredentialsRefreshBuffer = 5 * time.Minute

var ErrAuthenticationRequired = errors.New("proxy authentication required")

// Compile-time interface checks.
var (
	_ Client  = (*proxyClient)(nil)
	_ Service = (*proxyClient)(nil)
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

	// Set up auth client and credential store if OIDC is configured.
	issuerURL := strings.TrimRight(cfg.IssuerURL, "/")
	if issuerURL == "" {
		issuerURL = strings.TrimRight(cfg.URL, "/")
	}

	resource := strings.TrimRight(cfg.Resource, "/")

	if issuerURL != "" && cfg.ClientID != "" {
		c.authClient = client.New(log, client.Config{
			IssuerURL: issuerURL,
			ClientID:  cfg.ClientID,
			Resource:  resource,
			Username:  cfg.Username,
			Password:  cfg.Password,
		})

		// client_credentials mints tokens on demand and keeps them in
		// memory only — no on-disk credential store.
		if cfg.AuthMode != AuthModeClientCredentials {
			c.credStore = store.New(log, store.Config{
				AuthClient:      c.authClient,
				IssuerURL:       issuerURL,
				ClientID:        cfg.ClientID,
				Resource:        resource,
				RefreshTokenTTL: cfg.RefreshTokenTTL,
			})
		}
	}

	return c
}

// usesClientCredentials reports whether this client mints tokens via the
// client_credentials grant.
func (c *proxyClient) usesClientCredentials() bool {
	return c.cfg.AuthMode == AuthModeClientCredentials && c.authClient != nil
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
	if c.credStore == nil && !c.usesClientCredentials() {
		return NoAuthToken
	}

	token, err := c.loadAccessToken()
	if err != nil {
		if errors.Is(err, ErrAuthenticationRequired) {
			c.log.WithError(err).Debug("Proxy access token is not available")
		} else {
			c.log.WithError(err).Error("Failed to get proxy access token from credential store")
		}

		return ""
	}

	return token
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

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, strings.NewReader(sql))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
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
		return nil, fmt.Errorf("executing query: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("query failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return body, nil
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

// EmbeddingModel returns the configured embedding model name.
func (c *proxyClient) EmbeddingModel() string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.datasources.EmbeddingModel
}

// Discover fetches datasource information from the proxy's /datasources endpoint.
// In client_credentials mode a 401/403 invalidates the cached token and the
// request is retried once with a freshly minted one (covers proxy-side
// revocation before the local expiry buffer kicks in).
func (c *proxyClient) Discover(ctx context.Context) error {
	err := c.discoverOnce(ctx)
	if err != nil && errors.Is(err, ErrAuthenticationRequired) && c.usesClientCredentials() {
		c.log.WithError(err).Debug("Proxy rejected client_credentials token; re-minting and retrying")
		c.invalidateClientCredentialsToken()

		return c.discoverOnce(ctx)
	}

	return err
}

// discoverOnce performs a single /datasources fetch.
func (c *proxyClient) discoverOnce(ctx context.Context) error {
	url := fmt.Sprintf("%s/datasources", c.cfg.URL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	token, err := c.loadAccessToken()
	if err != nil {
		if errors.Is(err, ErrAuthenticationRequired) {
			return err
		}

		return fmt.Errorf("loading access token: %w", err)
	}

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
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
func (c *proxyClient) EnsureAuthenticated(_ context.Context) error {
	if c.usesClientCredentials() {
		if _, err := c.loadAccessToken(); err != nil {
			return fmt.Errorf("authenticating to proxy via client_credentials: %w", err)
		}

		return nil
	}

	if c.credStore == nil {
		// No auth required (e.g., local dev mode).
		return nil
	}

	_, err := c.loadAccessToken()
	if err != nil {
		return fmt.Errorf(
			"not authenticated to proxy. Run `panda auth login` first: %w",
			err,
		)
	}

	return nil
}

func (c *proxyClient) loadAccessToken() (string, error) {
	if c.usesClientCredentials() {
		return c.clientCredentialsToken()
	}

	if c.credStore == nil {
		return "", nil
	}

	tokens, err := c.credStore.Load()
	if err != nil {
		return "", fmt.Errorf("loading stored credentials: %w", err)
	}

	if tokens == nil {
		return "", ErrAuthenticationRequired
	}

	token, err := c.credStore.GetAccessToken()
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrAuthenticationRequired, err)
	}

	return token, nil
}

// clientCredentialsToken returns the cached client_credentials access token,
// re-minting via the issuer's token endpoint when missing or close to expiry.
// Tokens live in memory only; nothing is written to disk.
func (c *proxyClient) clientCredentialsToken() (string, error) {
	c.ccMu.Lock()
	defer c.ccMu.Unlock()

	if c.ccTokens != nil && time.Now().Add(clientCredentialsRefreshBuffer).Before(c.ccTokens.ExpiresAt) {
		return c.ccTokens.AccessToken, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.HTTPTimeout)
	defer cancel()

	tokens, err := c.authClient.ClientCredentials(ctx)
	if err != nil {
		// Keep serving a still-valid cached token across transient mint failures.
		if c.ccTokens != nil && time.Now().Before(c.ccTokens.ExpiresAt) {
			c.log.WithError(err).Warn("Re-minting client_credentials token failed; using cached token")
			return c.ccTokens.AccessToken, nil
		}

		return "", fmt.Errorf("minting client_credentials token: %w", err)
	}

	c.ccTokens = tokens

	return tokens.AccessToken, nil
}

// invalidateClientCredentialsToken drops the cached client_credentials token
// so the next loadAccessToken mints a fresh one. Used when the proxy rejects
// a token that has not yet hit the local expiry buffer (e.g. revocation).
func (c *proxyClient) invalidateClientCredentialsToken() {
	c.ccMu.Lock()
	defer c.ccMu.Unlock()

	c.ccTokens = nil
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
