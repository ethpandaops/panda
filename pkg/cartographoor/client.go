package cartographoor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ethpandaops/cartographoor/pkg/discovery"
	"github.com/sirupsen/logrus"
)

const (
	// DefaultCartographoorURL is the default URL for fetching network data.
	DefaultCartographoorURL = "https://ethpandaops-platform-production-cartographoor.ams3.digitaloceanspaces.com/networks.json"

	// DefaultCacheTTL is the default cache duration.
	DefaultCacheTTL = 5 * time.Minute

	// DefaultHTTPTimeout is the default HTTP request timeout.
	DefaultHTTPTimeout = 30 * time.Second
)

// groupPattern extracts group name from repository (e.g., "ethpandaops/fusaka-devnets" -> "fusaka").
var groupPattern = regexp.MustCompile(`ethpandaops/([a-z0-9-]+)-devnets`)

// CartographoorConfig holds configuration for the cartographoor client.
type CartographoorConfig struct {
	URL      string
	CacheTTL time.Duration
	Timeout  time.Duration
}

// CartographoorClient fetches and caches network data from cartographoor.
type CartographoorClient interface {
	// Start initializes the client and fetches initial data.
	Start(ctx context.Context) error
	// Stop stops background refresh.
	Stop() error
	// GetAllNetworks returns all networks.
	GetAllNetworks() map[string]discovery.Network
	// GetActiveNetworks returns only active networks.
	GetActiveNetworks() map[string]discovery.Network
	// GetNetwork returns a single network by name.
	GetNetwork(name string) (discovery.Network, bool)
	// GetGroup returns all networks in a devnet group.
	GetGroup(name string) (map[string]discovery.Network, bool)
	// GetGroups returns all available devnet group names.
	GetGroups() []string
	// IsDevnet returns true if the network is a devnet.
	IsDevnet(network discovery.Network) bool
	// GetClusters returns the xatu clusters for a network.
	GetClusters(network discovery.Network) []string
}

type cartographoorClient struct {
	log    logrus.FieldLogger
	cfg    CartographoorConfig
	client *http.Client

	mu          sync.RWMutex
	networks    map[string]discovery.Network
	groups      map[string][]string // group name -> network names
	lastUpdated time.Time

	done chan struct{}
	wg   sync.WaitGroup
}

// NewCartographoorClient creates a new cartographoor client.
func NewCartographoorClient(log logrus.FieldLogger, cfg CartographoorConfig) CartographoorClient {
	if cfg.URL == "" {
		cfg.URL = DefaultCartographoorURL
	}

	if cfg.CacheTTL == 0 {
		cfg.CacheTTL = DefaultCacheTTL
	}

	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultHTTPTimeout
	}

	return &cartographoorClient{
		log: log.WithField("component", "cartographoor"),
		cfg: cfg,
		client: &http.Client{
			Timeout: cfg.Timeout,
		},
		networks: make(map[string]discovery.Network),
		groups:   make(map[string][]string),
		done:     make(chan struct{}),
	}
}

// Start initializes the client and starts background refresh.
func (c *cartographoorClient) Start(ctx context.Context) error {
	c.log.WithField("url", c.cfg.URL).Info("Starting cartographoor client")

	// Initial fetch
	if err := c.refresh(ctx); err != nil {
		return fmt.Errorf("initial fetch failed: %w", err)
	}

	// Start background refresh
	c.wg.Add(1)

	go c.backgroundRefresh()

	c.log.WithFields(logrus.Fields{
		"network_count": len(c.networks),
		"group_count":   len(c.groups),
		"cache_ttl":     c.cfg.CacheTTL,
	}).Info("Cartographoor client started")

	return nil
}

// Stop stops the background refresh goroutine.
func (c *cartographoorClient) Stop() error {
	close(c.done)
	c.wg.Wait()

	c.log.Info("Cartographoor client stopped")

	return nil
}

// GetAllNetworks returns all networks.
func (c *cartographoorClient) GetAllNetworks() map[string]discovery.Network {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make(map[string]discovery.Network, len(c.networks))
	for k, v := range c.networks {
		result[k] = v
	}

	return result
}

// GetActiveNetworks returns only active networks.
func (c *cartographoorClient) GetActiveNetworks() map[string]discovery.Network {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make(map[string]discovery.Network, len(c.networks))

	for k, v := range c.networks {
		if v.Status == "active" {
			result[k] = v
		}
	}

	return result
}

// GetNetwork returns a single network by name.
func (c *cartographoorClient) GetNetwork(name string) (discovery.Network, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	network, ok := c.networks[name]

	return network, ok
}

// GetGroup returns all networks in a devnet group.
func (c *cartographoorClient) GetGroup(name string) (map[string]discovery.Network, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	networkNames, ok := c.groups[name]
	if !ok {
		return nil, false
	}

	result := make(map[string]discovery.Network, len(networkNames))

	for _, netName := range networkNames {
		if network, exists := c.networks[netName]; exists {
			result[netName] = network
		}
	}

	return result, true
}

// GetGroups returns all available devnet group names.
func (c *cartographoorClient) GetGroups() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	groups := make([]string, 0, len(c.groups))

	for name := range c.groups {
		groups = append(groups, name)
	}

	sort.Strings(groups)

	return groups
}

// IsDevnet returns true if the network is a devnet.
func (c *cartographoorClient) IsDevnet(network discovery.Network) bool {
	return strings.Contains(network.Repository, "devnet")
}

// GetClusters returns the xatu clusters for a network.
func (c *cartographoorClient) GetClusters(network discovery.Network) []string {
	if c.IsDevnet(network) {
		return []string{"xatu-experimental", "xatu-cbt"}
	}

	return []string{"xatu", "xatu-cbt"}
}

// backgroundRefresh periodically refreshes the network data.
func (c *cartographoorClient) backgroundRefresh() {
	defer c.wg.Done()

	ticker := time.NewTicker(c.cfg.CacheTTL)
	defer ticker.Stop()

	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), c.cfg.Timeout)

			if err := c.refresh(ctx); err != nil {
				c.log.WithError(err).Warn("Failed to refresh network data")
			} else {
				c.log.WithField("network_count", len(c.networks)).Debug("Refreshed network data")
			}

			cancel()
		}
	}
}

// refresh fetches the latest network data from cartographoor.
func (c *cartographoorClient) refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.URL, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetching data: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var result discovery.Result
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	// Build groups map
	groups := make(map[string][]string, 16)

	for name, network := range result.Networks {
		if matches := groupPattern.FindStringSubmatch(network.Repository); len(matches) == 2 {
			groupName := matches[1]
			groups[groupName] = append(groups[groupName], name)
		}
	}

	// Sort network names within each group
	for _, names := range groups {
		sort.Strings(names)
	}

	// Update cache
	c.mu.Lock()
	c.networks = result.Networks
	c.groups = groups
	c.lastUpdated = time.Now()
	c.mu.Unlock()

	return nil
}
