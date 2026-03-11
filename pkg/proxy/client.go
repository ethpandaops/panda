package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/internal/version"
	"github.com/ethpandaops/panda/pkg/auth/client"
	"github.com/ethpandaops/panda/pkg/auth/store"
	"github.com/ethpandaops/panda/pkg/serverapi"
	"github.com/ethpandaops/panda/pkg/types"
)

// Client connects to a proxy server and provides datasource discovery plus
// proxy-scoped bearer tokens for server-to-proxy calls.
type Client interface {
	// Start starts the client and performs initial discovery.
	Start(ctx context.Context) error

	// Stop stops the client.
	Stop(ctx context.Context) error

	// URL returns the proxy URL.
	URL() string

	OutboundAuthorizer

	// RegisterToken returns the current proxy access token for server-to-proxy calls.
	RegisterToken(executionID string) string

	// RevokeToken is a no-op for client-managed bearer tokens.
	RevokeToken(executionID string)

	// ClickHouseDatasources returns the discovered ClickHouse datasource names.
	ClickHouseDatasources() []string
	// ClickHouseDatasourceInfo returns detailed ClickHouse datasource info.
	ClickHouseDatasourceInfo() []types.DatasourceInfo

	// PrometheusDatasources returns the discovered Prometheus datasource names.
	PrometheusDatasources() []string
	// PrometheusDatasourceInfo returns detailed Prometheus datasource info.
	PrometheusDatasourceInfo() []types.DatasourceInfo

	// LokiDatasources returns the discovered Loki datasource names.
	LokiDatasources() []string
	// LokiDatasourceInfo returns detailed Loki datasource info.
	LokiDatasourceInfo() []types.DatasourceInfo

	// S3Bucket returns the discovered S3 bucket name.
	S3Bucket() string

	// S3PublicURLPrefix returns the discovered S3 public URL prefix.
	S3PublicURLPrefix() string

	// EthNodeAvailable returns true if the proxy has ethnode credentials configured.
	EthNodeAvailable() bool

	// DatasourceInfo returns all discovered datasource metadata.
	DatasourceInfo() []types.DatasourceInfo

	// Discover fetches datasource information from the proxy.
	Discover(ctx context.Context) error

	// EnsureAuthenticated checks if the user has valid credentials.
	EnsureAuthenticated(ctx context.Context) error
}

// ClientConfig configures the proxy client.
type ClientConfig struct {
	// URL is the base URL of the proxy server (e.g., http://localhost:18081).
	URL string

	// IssuerURL is the OAuth issuer URL for proxy authentication.
	// If empty, URL is used and the client will only work against auth.mode=none proxies.
	IssuerURL string

	// ClientID is the OAuth client ID for authentication.
	ClientID string

	// Resource is the OAuth protected resource to request tokens for.
	// Defaults to URL when omitted.
	Resource string

	// DiscoveryInterval is how often to refresh datasource info (default: 5 minutes).
	// Set to 0 to disable background refresh.
	DiscoveryInterval time.Duration

	// HTTPTimeout is the timeout for HTTP requests (default: 30 seconds).
	HTTPTimeout time.Duration
}

// ApplyDefaults sets default values for the client config.
func (c *ClientConfig) ApplyDefaults() {
	if c.DiscoveryInterval == 0 {
		c.DiscoveryInterval = 5 * time.Minute
	}

	if c.HTTPTimeout == 0 {
		c.HTTPTimeout = 30 * time.Second
	}
}

// proxyClient implements Client for connecting to a proxy server.
type proxyClient struct {
	log        logrus.FieldLogger
	cfg        ClientConfig
	httpClient *http.Client
	authClient client.Client
	credStore  store.Store

	mu          sync.RWMutex
	datasources *serverapi.DatasourcesResponse
	stopCh      chan struct{}
	stopped     bool
}

var ErrAuthenticationRequired = errors.New("proxy authentication required")

// Compile-time interface checks.
var (
	_ Client  = (*proxyClient)(nil)
	_ Service = (*proxyClient)(nil)
)

// NewClient creates a new proxy client.
func NewClient(log logrus.FieldLogger, cfg ClientConfig) Client {
	cfg.ApplyDefaults()

	c := &proxyClient{
		log: log.WithField("component", "proxy-client"),
		cfg: cfg,
		httpClient: &http.Client{
			Transport: &version.Transport{},
			Timeout:   cfg.HTTPTimeout,
		},
		datasources: &serverapi.DatasourcesResponse{},
		stopCh:      make(chan struct{}),
	}

	// Set up auth client and credential store if OIDC is configured.
	issuerURL := strings.TrimRight(cfg.IssuerURL, "/")
	if issuerURL == "" {
		issuerURL = strings.TrimRight(cfg.URL, "/")
	}

	resource := strings.TrimRight(cfg.Resource, "/")
	if resource == "" {
		resource = strings.TrimRight(cfg.URL, "/")
	}

	if issuerURL != "" && cfg.ClientID != "" {
		c.authClient = client.New(log, client.Config{
			IssuerURL: issuerURL,
			ClientID:  cfg.ClientID,
			Resource:  resource,
		})

		c.credStore = store.New(log, store.Config{
			AuthClient: c.authClient,
			IssuerURL:  issuerURL,
			ClientID:   cfg.ClientID,
			Resource:   resource,
		})
	}

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
			return fmt.Errorf("initial discovery failed: %w", err)
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

func (c *proxyClient) RegisterToken(_ string) string {
	if c.credStore == nil {
		return "none"
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

func (c *proxyClient) RevokeToken(_ string) {
	// No-op: tokens are managed by the proxy control plane.
}

// AuthorizeRequest attaches the current proxy bearer token to req.
// Existing Authorization headers are preserved so caller-provided auth wins.
func (c *proxyClient) AuthorizeRequest(req *http.Request) error {
	if req == nil {
		return fmt.Errorf("request is required")
	}

	if req.Header.Get("Authorization") != "" {
		return nil
	}

	token, err := c.loadAccessToken()
	if err != nil {
		return fmt.Errorf("authorizing proxy request: %w", err)
	}

	if token != "" && token != "none" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	return nil
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

func datasourceInfoByType(infos []types.DatasourceInfo, kind string) []types.DatasourceInfo {
	if len(infos) == 0 {
		return nil
	}

	filtered := make([]types.DatasourceInfo, 0, len(infos))
	for _, info := range infos {
		if info.Type != kind || info.Name == "" {
			continue
		}

		filtered = append(filtered, info)
	}

	return filtered
}

func datasourceCountByType(infos []types.DatasourceInfo, kind string) int {
	return len(datasourceInfoByType(infos, kind))
}

// ClickHouseDatasources returns the discovered ClickHouse datasource names.
func (c *proxyClient) ClickHouseDatasources() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return namesFromInfo(datasourceInfoByType(c.datasources.Datasources, "clickhouse"))
}

// ClickHouseDatasourceInfo returns detailed ClickHouse datasource info.
func (c *proxyClient) ClickHouseDatasourceInfo() []types.DatasourceInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return datasourceInfoByType(c.datasources.Datasources, "clickhouse")
}

// PrometheusDatasources returns the discovered Prometheus datasource names.
func (c *proxyClient) PrometheusDatasources() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return namesFromInfo(datasourceInfoByType(c.datasources.Datasources, "prometheus"))
}

// PrometheusDatasourceInfo returns detailed Prometheus datasource info.
func (c *proxyClient) PrometheusDatasourceInfo() []types.DatasourceInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return datasourceInfoByType(c.datasources.Datasources, "prometheus")
}

// LokiDatasources returns the discovered Loki datasource names.
func (c *proxyClient) LokiDatasources() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return namesFromInfo(datasourceInfoByType(c.datasources.Datasources, "loki"))
}

// LokiDatasourceInfo returns detailed Loki datasource info.
func (c *proxyClient) LokiDatasourceInfo() []types.DatasourceInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return datasourceInfoByType(c.datasources.Datasources, "loki")
}

// S3Bucket returns the discovered S3 bucket name.
func (c *proxyClient) S3Bucket() string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.datasources.S3Bucket
}

// S3PublicURLPrefix returns the discovered S3 public URL prefix.
func (c *proxyClient) S3PublicURLPrefix() string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.datasources.S3PublicURLPrefix
}

// EthNodeAvailable returns true if the proxy has ethnode credentials configured.
func (c *proxyClient) EthNodeAvailable() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.datasources.EthNodeAvailable
}

// DatasourceInfo returns all discovered datasource metadata.
func (c *proxyClient) DatasourceInfo() []types.DatasourceInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return cloneDatasourceInfo(c.datasources.Datasources)
}

// Discover fetches datasource information from the proxy's /datasources endpoint.
func (c *proxyClient) Discover(ctx context.Context) error {
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

	var datasources serverapi.DatasourcesResponse
	if err := json.NewDecoder(resp.Body).Decode(&datasources); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	c.mu.Lock()
	c.datasources = &datasources
	c.mu.Unlock()

	c.log.WithFields(logrus.Fields{
		"clickhouse": datasourceCountByType(datasources.Datasources, "clickhouse"),
		"prometheus": datasourceCountByType(datasources.Datasources, "prometheus"),
		"loki":       datasourceCountByType(datasources.Datasources, "loki"),
		"s3_bucket":  datasources.S3Bucket,
	}).Debug("Discovered datasources from proxy")

	return nil
}

// EnsureAuthenticated checks if the user has valid credentials.
func (c *proxyClient) EnsureAuthenticated(_ context.Context) error {
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
