package cartographoor

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ethpandaops/cartographoor/pkg/client"
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
	// GetClusters returns the clickhouse clusters for a network.
	GetClusters(network discovery.Network) []string
}

// cartographoorClient adapts the upstream cartographoor client library to panda's
// network model. Fetching and caching of networks.json is delegated to a
// client.MemoryProvider; this type layers panda-specific devnet grouping and
// clickhouse cluster mapping on top, refreshing its derived state whenever the
// provider reports new data.
type cartographoorClient struct {
	log logrus.FieldLogger
	cfg CartographoorConfig

	provider client.Provider

	mu       sync.RWMutex
	networks map[string]discovery.Network
	groups   map[string][]string // group name -> network names

	done chan struct{}
	wg   sync.WaitGroup
}

// NewCartographoorClient creates a new cartographoor client.
func NewCartographoorClient(log logrus.FieldLogger, cfg CartographoorConfig) CartographoorClient {
	return newClient(log, cfg)
}

// newClient builds the concrete client with defaults applied. It returns the
// concrete type (rather than the interface) so tests can inject an
// already-started provider via startWithProvider.
func newClient(log logrus.FieldLogger, cfg CartographoorConfig) *cartographoorClient {
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
		log:      log.WithField("component", "cartographoor"),
		cfg:      cfg,
		networks: make(map[string]discovery.Network),
		groups:   make(map[string][]string),
		done:     make(chan struct{}),
	}
}

// Start initializes the underlying provider and starts watching for refreshes.
func (c *cartographoorClient) Start(ctx context.Context) error {
	c.log.WithField("url", c.cfg.URL).Info("Starting cartographoor client")

	provider, err := client.NewMemoryProvider(client.Config{
		SourceURL:       c.cfg.URL,
		RefreshInterval: c.cfg.CacheTTL,
		RequestTimeout:  c.cfg.Timeout,
	}, c.log)
	if err != nil {
		return fmt.Errorf("creating provider: %w", err)
	}

	// Start blocks until the initial fetch completes (or fails).
	if err := provider.Start(ctx); err != nil {
		return fmt.Errorf("initial fetch failed: %w", err)
	}

	return c.startWithProvider(ctx, provider)
}

// startWithProvider wires an already-started provider into the client: it builds
// the initial derived state and launches the watcher that rebuilds whenever the
// provider reports new data. Separated from Start so tests can inject a provider.
func (c *cartographoorClient) startWithProvider(ctx context.Context, provider client.Provider) error {
	c.provider = provider

	// Build the initial derived state from the freshly fetched data.
	if err := c.rebuild(ctx); err != nil {
		return fmt.Errorf("building network cache: %w", err)
	}

	// Watch for subsequent provider refreshes and rebuild derived state.
	c.wg.Add(1)

	go c.watch(ctx)

	c.mu.RLock()
	networkCount, groupCount := len(c.networks), len(c.groups)
	c.mu.RUnlock()

	c.log.WithFields(logrus.Fields{
		"network_count": networkCount,
		"group_count":   groupCount,
		"cache_ttl":     c.cfg.CacheTTL,
	}).Info("Cartographoor client started")

	return nil
}

// Stop stops the watch goroutine and the underlying provider.
func (c *cartographoorClient) Stop() error {
	close(c.done)
	c.wg.Wait()

	if c.provider != nil {
		if err := c.provider.Stop(); err != nil {
			return fmt.Errorf("stopping provider: %w", err)
		}
	}

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

// GetClusters returns the clickhouse clusters for a network.
func (c *cartographoorClient) GetClusters(network discovery.Network) []string {
	if c.IsDevnet(network) {
		return []string{"xatu-experimental", "clickhouse-refined"}
	}

	return []string{"clickhouse-raw", "clickhouse-refined"}
}

// watch rebuilds the derived state whenever the provider reports new data.
func (c *cartographoorClient) watch(ctx context.Context) {
	defer c.wg.Done()

	notify := c.provider.NotifyChannel()

	for {
		select {
		case <-c.done:
			return
		case <-ctx.Done():
			return
		case <-notify:
			if err := c.rebuild(ctx); err != nil {
				c.log.WithError(err).Warn("Failed to refresh network data")

				continue
			}

			c.mu.RLock()
			count := len(c.networks)
			c.mu.RUnlock()

			c.log.WithField("network_count", count).Debug("Refreshed network data")
		}
	}
}

// rebuild reads networks from the provider and recomputes the local network
// cache and devnet group index.
func (c *cartographoorClient) rebuild(ctx context.Context) error {
	networks, err := c.provider.GetNetworks(ctx)
	if err != nil {
		return fmt.Errorf("fetching networks: %w", err)
	}

	groups := buildGroups(networks)

	c.mu.Lock()
	c.networks = networks
	c.groups = groups
	c.mu.Unlock()

	return nil
}

// buildGroups indexes networks into devnet groups (group name -> sorted network names)
// based on the "ethpandaops/{group}-devnets" repository naming convention.
func buildGroups(networks map[string]discovery.Network) map[string][]string {
	groups := make(map[string][]string, 16)

	for name, network := range networks {
		if matches := groupPattern.FindStringSubmatch(network.Repository); len(matches) == 2 {
			groupName := matches[1]
			groups[groupName] = append(groups[groupName], name)
		}
	}

	// Sort network names within each group for stable output.
	for _, names := range groups {
		sort.Strings(names)
	}

	return groups
}
