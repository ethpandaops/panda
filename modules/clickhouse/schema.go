package clickhouse

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/proxy"
)

const (
	// DefaultSchemaRefreshInterval is the refresh interval for schema discovery.
	DefaultSchemaRefreshInterval = 15 * time.Minute

	// DefaultSchemaQueryTimeout is the timeout for individual schema queries.
	DefaultSchemaQueryTimeout = 60 * time.Second

	// schemaQueryConcurrency limits concurrent schema queries per cluster.
	schemaQueryConcurrency = 5
)

// ClickHouseSchemaConfig holds configuration for schema discovery.
type ClickHouseSchemaConfig struct {
	RefreshInterval time.Duration
	QueryTimeout    time.Duration
	Datasources     []SchemaDiscoveryDatasource
}

// TableColumn represents a column in a ClickHouse table.
type TableColumn struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	Comment      string `json:"comment,omitempty"`
	DefaultType  string `json:"default_type,omitempty"`
	DefaultValue string `json:"default_value,omitempty"`
}

// TableSchema represents the full schema of a ClickHouse table.
type TableSchema struct {
	Name            string        `json:"name"`
	Engine          string        `json:"engine,omitempty"`
	Columns         []TableColumn `json:"columns"`
	Networks        []string      `json:"networks,omitempty"`
	HasNetworkCol   bool          `json:"has_network_column"`
	CreateStatement string        `json:"create_statement,omitempty"`
	Comment         string        `json:"comment,omitempty"`
}

// ClusterTables represents tables available in a ClickHouse cluster.
type ClusterTables struct {
	ClusterName string                  `json:"cluster_name"`
	Tables      map[string]*TableSchema `json:"tables"`
	LastUpdated time.Time               `json:"last_updated"`
}

// ClickHouseSchemaClient fetches and caches ClickHouse schema information.
type ClickHouseSchemaClient interface {
	// Start initializes the client and kicks off the initial schema refresh asynchronously.
	Start(ctx context.Context) error
	// Stop stops background refresh.
	Stop() error
	// WaitForReady blocks until the first asynchronous schema refresh attempt completes or ctx is cancelled.
	WaitForReady(ctx context.Context) error
	// GetAllTables returns all tables across all clusters.
	GetAllTables() map[string]*ClusterTables
	// GetTable returns schema for a specific table (searches all clusters).
	GetTable(tableName string) (*TableSchema, string, bool)
}

// Compile-time interface compliance check.
var _ ClickHouseSchemaClient = (*clickhouseSchemaClient)(nil)

type clickhouseSchemaClient struct {
	log         logrus.FieldLogger
	cfg         ClickHouseSchemaConfig
	queryClient clickhouseSchemaQueryClient

	mu          sync.RWMutex
	clusters    map[string]*ClusterTables
	datasources map[string]string // cluster name -> datasource name

	done  chan struct{}
	ready chan struct{} // closed when initial fetch completes
	wg    sync.WaitGroup
}

// NewClickHouseSchemaClient creates a new schema discovery client.
func NewClickHouseSchemaClient(
	log logrus.FieldLogger,
	cfg ClickHouseSchemaConfig,
	proxySvc proxy.ClickHouseSchemaAccess,
) ClickHouseSchemaClient {
	if cfg.RefreshInterval == 0 {
		cfg.RefreshInterval = DefaultSchemaRefreshInterval
	}

	if cfg.QueryTimeout == 0 {
		cfg.QueryTimeout = DefaultSchemaQueryTimeout
	}

	return &clickhouseSchemaClient{
		log:         log.WithField("component", "clickhouse_schema"),
		cfg:         cfg,
		queryClient: newClickhouseSchemaQueryClient(proxySvc, &http.Client{}, cfg.QueryTimeout),
		clusters:    make(map[string]*ClusterTables, 2),
		datasources: make(map[string]string, 2),
		done:        make(chan struct{}),
		ready:       make(chan struct{}),
	}
}

// Start initializes the client and starts background refresh.
// The initial schema fetch runs asynchronously to avoid blocking server startup.
func (c *clickhouseSchemaClient) Start(ctx context.Context) error {
	c.log.WithField("refresh_interval", c.cfg.RefreshInterval).Info("Starting ClickHouse schema client")

	// Initialize proxy-backed datasource mappings.
	if err := c.initDatasources(); err != nil {
		return fmt.Errorf("initializing ClickHouse datasources: %w", err)
	}

	// Start background refresh (includes initial fetch)
	c.wg.Add(1)

	go c.backgroundRefresh()

	// Trigger immediate initial fetch (tracked to prevent use-after-close)
	c.wg.Add(1)

	go func() {
		defer c.wg.Done()
		defer close(c.ready)

		fetchCtx, cancel := context.WithTimeout(context.Background(), c.cfg.QueryTimeout*10)
		defer cancel()

		if err := c.refresh(fetchCtx); err != nil {
			c.log.WithError(err).Warn("Initial schema fetch failed, will retry on next refresh interval")
		} else {
			tableCount := 0
			clusterCount := 0

			c.mu.RLock()
			clusterCount = len(c.clusters)
			for _, cluster := range c.clusters {
				tableCount += len(cluster.Tables)
			}
			c.mu.RUnlock()

			c.log.WithFields(logrus.Fields{
				"cluster_count": clusterCount,
				"table_count":   tableCount,
			}).Info("Initial ClickHouse schema fetch completed")
		}
	}()

	c.log.Info("ClickHouse schema client started (fetching schema in background)")

	return nil
}

// initDatasources initializes proxy-backed datasource mappings.
func (c *clickhouseSchemaClient) initDatasources() error {
	if c.queryClient.proxySvc == nil {
		return fmt.Errorf("proxy service is required for schema discovery")
	}

	for _, ds := range c.cfg.Datasources {
		if ds.Name == "" || ds.Cluster == "" {
			continue
		}

		if _, exists := c.datasources[ds.Cluster]; exists {
			c.log.WithFields(logrus.Fields{
				"name":    ds.Name,
				"cluster": ds.Cluster,
			}).Warn("Duplicate schema discovery cluster name; keeping first entry")

			continue
		}

		c.datasources[ds.Cluster] = ds.Name

		c.log.WithFields(logrus.Fields{
			"name":    ds.Name,
			"cluster": ds.Cluster,
		}).Debug("Configured ClickHouse schema discovery datasource")
	}

	if len(c.datasources) == 0 {
		return fmt.Errorf("no ClickHouse schema discovery datasources configured")
	}

	return nil
}

// Stop stops the background refresh goroutine.
func (c *clickhouseSchemaClient) Stop() error {
	close(c.done)
	c.wg.Wait()

	c.log.Info("ClickHouse schema client stopped")

	return nil
}

// WaitForReady blocks until the first asynchronous schema refresh attempt completes or ctx is cancelled.
func (c *clickhouseSchemaClient) WaitForReady(ctx context.Context) error {
	select {
	case <-c.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// GetAllTables returns all tables across all clusters.
func (c *clickhouseSchemaClient) GetAllTables() map[string]*ClusterTables {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make(map[string]*ClusterTables, len(c.clusters))
	for k, v := range c.clusters {
		// Deep copy cluster tables.
		clusterCopy := &ClusterTables{
			ClusterName: v.ClusterName,
			Tables:      make(map[string]*TableSchema, len(v.Tables)),
			LastUpdated: v.LastUpdated,
		}

		for tableName, schema := range v.Tables {
			clusterCopy.Tables[tableName] = schema
		}

		result[k] = clusterCopy
	}

	return result
}

// GetTable returns schema for a specific table (searches all clusters).
func (c *clickhouseSchemaClient) GetTable(tableName string) (*TableSchema, string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for clusterName, cluster := range c.clusters {
		if schema, ok := cluster.Tables[tableName]; ok {
			return schema, clusterName, true
		}
	}

	return nil, "", false
}

// backgroundRefresh periodically refreshes the schema data.
func (c *clickhouseSchemaClient) backgroundRefresh() {
	defer c.wg.Done()

	ticker := time.NewTicker(c.cfg.RefreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			c.doRefresh()
		}
	}
}

// doRefresh performs a single schema refresh with proper context cleanup.
func (c *clickhouseSchemaClient) doRefresh() {
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.QueryTimeout*10)
	defer cancel()

	if err := c.refresh(ctx); err != nil {
		c.log.WithError(err).Warn("Failed to refresh ClickHouse schema data")

		return
	}

	tableCount := 0

	c.mu.RLock()
	for _, cluster := range c.clusters {
		tableCount += len(cluster.Tables)
	}
	c.mu.RUnlock()

	c.log.WithField("table_count", tableCount).Debug("Refreshed ClickHouse schema data")
}

// refresh fetches the latest schema from all configured clusters.
func (c *clickhouseSchemaClient) refresh(ctx context.Context) error {
	if len(c.datasources) == 0 {
		c.log.Warn("No ClickHouse datasources available for schema discovery")

		return nil
	}

	newClusters := make(map[string]*ClusterTables, len(c.datasources))

	for clusterName, datasourceName := range c.datasources {
		tables, err := c.discoverClusterSchema(ctx, clusterName, datasourceName)
		if err != nil {
			c.log.WithError(err).WithField("cluster", clusterName).Warn("Failed to discover cluster schema")

			continue
		}

		newClusters[clusterName] = tables
	}

	// Atomic update.
	c.mu.Lock()
	c.clusters = newClusters
	c.mu.Unlock()

	return nil
}

// discoverClusterSchema discovers schema for a single cluster.
func (c *clickhouseSchemaClient) discoverClusterSchema(
	ctx context.Context,
	clusterName string,
	datasourceName string,
) (*ClusterTables, error) {
	queries := c.queryClient

	tables, err := queries.fetchTableList(ctx, datasourceName)
	if err != nil {
		return nil, fmt.Errorf("fetching table list: %w", err)
	}

	clusterTables := &ClusterTables{
		ClusterName: clusterName,
		Tables:      make(map[string]*TableSchema, len(tables)),
		LastUpdated: time.Now(),
	}

	// Get schema for each table with concurrency limit.
	sem := make(chan struct{}, schemaQueryConcurrency)

	var wg sync.WaitGroup

	var mu sync.Mutex

	for _, tableName := range tables {
		wg.Add(1)

		go func(name string) {
			defer wg.Done()

			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			schema, err := queries.fetchTableSchema(ctx, datasourceName, name)
			if err != nil {
				c.log.WithError(err).WithField("table", name).Debug("Failed to fetch table schema")

				return
			}

			// Get networks if table has meta_network_name column.
			if schema.HasNetworkCol {
				networks, err := queries.fetchTableNetworks(ctx, datasourceName, name)
				if err != nil {
					c.log.WithError(err).WithField("table", name).Debug("Failed to fetch table networks")
				} else {
					schema.Networks = networks
				}
			}

			mu.Lock()
			clusterTables.Tables[name] = schema
			mu.Unlock()
		}(tableName)
	}

	wg.Wait()

	return clusterTables, nil
}
