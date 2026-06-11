package clickhouse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/proxy"
)

// Pre-compiled regexes for schema parsing.
var (
	// validIdentifier matches valid ClickHouse table/column identifiers.
	validIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

	// enginePattern extracts the engine name from a CREATE TABLE statement.
	enginePattern = regexp.MustCompile(`ENGINE\s*=\s*(\w+)`)

	// tableCommentPattern extracts the table comment from a CREATE TABLE statement.
	tableCommentPattern = regexp.MustCompile(`COMMENT\s+'([^']*)'`)

	// columnPattern extracts column name and type from a column definition line.
	columnPattern = regexp.MustCompile("(?m)^\\s*`([^`]+)`\\s+([^,\\n]+)")

	// columnCommentPattern extracts the comment from a column definition.
	columnCommentPattern = regexp.MustCompile(`COMMENT\s+'([^']*)'`)

	// defaultPattern extracts the default expression from a column definition.
	defaultPattern = regexp.MustCompile(`(DEFAULT|MATERIALIZED|ALIAS)\s+([^,\n]+?)(?:\s+(?:CODEC|COMMENT|$))`)
)

const (
	// DefaultSchemaRefreshInterval is the refresh interval for schema discovery.
	DefaultSchemaRefreshInterval = 15 * time.Minute

	// DefaultSchemaQueryTimeout is the timeout for individual schema queries.
	DefaultSchemaQueryTimeout = 60 * time.Second

	// schemaQueryConcurrency limits concurrent schema queries per cluster.
	schemaQueryConcurrency = 5
)

// SchemaConfig holds configuration for schema discovery.
type SchemaConfig struct {
	RefreshInterval time.Duration
	QueryTimeout    time.Duration
	Datasources     []SchemaDiscoveryDatasource
}

type discoveredTable struct {
	Name     string
	Database string
}

// TableColumn represents a column in a ClickHouse table.
type TableColumn struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	Comment      string `json:"comment,omitempty"`
	DefaultType  string `json:"default_type,omitempty"`
	DefaultValue string `json:"default_value,omitempty"`
}

// TableSchema is the full schema of a ClickHouse table identified by (Database, Name).
type TableSchema struct {
	Database        string        `json:"database"`
	Name            string        `json:"name"`
	Engine          string        `json:"engine,omitempty"`
	PartitionBy     string        `json:"partition_by,omitempty"`
	OrderBy         string        `json:"order_by,omitempty"`
	PrimaryKey      string        `json:"primary_key,omitempty"`
	Columns         []TableColumn `json:"columns"`
	CreateStatement string        `json:"create_statement,omitempty"`
	Comment         string        `json:"comment,omitempty"`
}

// ClusterTables holds tables for a ClickHouse cluster, keyed by "<database>.<name>".
type ClusterTables struct {
	ClusterName string                  `json:"cluster_name"`
	Tables      map[string]*TableSchema `json:"tables"`
	LastUpdated time.Time               `json:"last_updated"`
}

func tableKey(database, name string) string {
	return database + "." + name
}

// SchemaClient fetches and caches ClickHouse schema information.
type SchemaClient interface {
	Start(ctx context.Context) error
	Stop() error
	GetAllTables() map[string]*ClusterTables
	GetClusterTables(clusterName string) (*ClusterTables, bool)
	GetTableInCluster(clusterName, database, tableName string) (*TableSchema, bool)
	FetchTablesInCluster(ctx context.Context, clusterName, database string) (*ClusterTables, error)
	FetchTableInCluster(ctx context.Context, clusterName, database, tableName string) (*TableSchema, error)
	UpdateDatasources(datasources []SchemaDiscoveryDatasource)
}

// Compile-time interface compliance check.
var _ SchemaClient = (*clickhouseSchemaClient)(nil)

type clickhouseSchemaClient struct {
	log      logrus.FieldLogger
	cfg      SchemaConfig
	proxySvc proxy.Service

	mu       sync.RWMutex
	clusters map[string]*ClusterTables

	dsMu        sync.RWMutex
	datasources map[string]string // cluster name -> datasource name

	done       chan struct{}
	refreshNow chan struct{} // capacity 1; coalesces on-demand refresh signals
	wg         sync.WaitGroup
}

// NewSchemaClient creates a new schema discovery client.
func NewSchemaClient(
	log logrus.FieldLogger,
	cfg SchemaConfig,
	proxySvc proxy.Service,
) SchemaClient {
	if cfg.RefreshInterval == 0 {
		cfg.RefreshInterval = DefaultSchemaRefreshInterval
	}

	if cfg.QueryTimeout == 0 {
		cfg.QueryTimeout = DefaultSchemaQueryTimeout
	}

	return &clickhouseSchemaClient{
		log:         log.WithField("component", "clickhouse_schema"),
		cfg:         cfg,
		proxySvc:    proxySvc,
		clusters:    make(map[string]*ClusterTables, 2),
		datasources: make(map[string]string, 2),
		done:        make(chan struct{}),
		refreshNow:  make(chan struct{}, 1),
	}
}

// UpdateDatasources replaces the cluster→datasource mapping used on the next
// refresh and signals an immediate refresh.
func (c *clickhouseSchemaClient) UpdateDatasources(datasources []SchemaDiscoveryDatasource) {
	newMap := make(map[string]string, len(datasources))

	for _, ds := range datasources {
		if ds.Name == "" || ds.Cluster == "" {
			continue
		}

		if _, exists := newMap[ds.Cluster]; exists {
			c.log.WithFields(logrus.Fields{
				"name":    ds.Name,
				"cluster": ds.Cluster,
			}).Warn("Duplicate schema discovery cluster name; keeping first entry")

			continue
		}

		newMap[ds.Cluster] = ds.Name
	}

	c.dsMu.Lock()
	previous := c.datasources
	c.datasources = newMap
	c.dsMu.Unlock()

	if datasourceMapsEqual(previous, newMap) {
		return
	}

	c.log.WithField("cluster_count", len(newMap)).Info("ClickHouse schema datasources updated; triggering refresh")

	select {
	case c.refreshNow <- struct{}{}:
	default:
		// Refresh already pending.
	}
}

func datasourceMapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}

	for k, v := range a {
		if b[k] != v {
			return false
		}
	}

	return true
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
	if c.proxySvc == nil {
		return fmt.Errorf("proxy service is required for schema discovery")
	}

	initial := make(map[string]string, len(c.cfg.Datasources))

	for _, ds := range c.cfg.Datasources {
		if ds.Name == "" || ds.Cluster == "" {
			continue
		}

		if _, exists := initial[ds.Cluster]; exists {
			c.log.WithFields(logrus.Fields{
				"name":    ds.Name,
				"cluster": ds.Cluster,
			}).Warn("Duplicate schema discovery cluster name; keeping first entry")

			continue
		}

		initial[ds.Cluster] = ds.Name

		c.log.WithFields(logrus.Fields{
			"name":    ds.Name,
			"cluster": ds.Cluster,
		}).Debug("Configured ClickHouse schema discovery datasource")
	}

	c.dsMu.Lock()
	c.datasources = initial
	c.dsMu.Unlock()

	if len(initial) == 0 {
		return fmt.Errorf("no ClickHouse schema discovery datasources configured")
	}

	return nil
}

// snapshotDatasources returns a copy of the current cluster→datasource mapping
// so the refresh loop can iterate without holding the lock across network calls.
func (c *clickhouseSchemaClient) snapshotDatasources() map[string]string {
	c.dsMu.RLock()
	defer c.dsMu.RUnlock()

	out := make(map[string]string, len(c.datasources))
	for k, v := range c.datasources {
		out[k] = v
	}

	return out
}

// Stop stops the background refresh goroutine.
func (c *clickhouseSchemaClient) Stop() error {
	close(c.done)
	c.wg.Wait()

	c.log.Info("ClickHouse schema client stopped")

	return nil
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

// GetClusterTables returns a copy of the tables for a single cluster.
func (c *clickhouseSchemaClient) GetClusterTables(clusterName string) (*ClusterTables, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	cluster, ok := c.clusters[clusterName]
	if !ok {
		return nil, false
	}

	clusterCopy := &ClusterTables{
		ClusterName: cluster.ClusterName,
		Tables:      make(map[string]*TableSchema, len(cluster.Tables)),
		LastUpdated: cluster.LastUpdated,
	}

	for tableName, schema := range cluster.Tables {
		clusterCopy.Tables[tableName] = schema
	}

	return clusterCopy, true
}

// GetTableInCluster returns the schema for a table in a specific cluster.
func (c *clickhouseSchemaClient) GetTableInCluster(clusterName, database, tableName string) (*TableSchema, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	cluster, ok := c.clusters[clusterName]
	if !ok {
		return nil, false
	}

	schema, ok := cluster.Tables[tableKey(database, tableName)]
	if !ok {
		return nil, false
	}

	return schema, true
}

// ErrSchemaClusterNotConfigured is returned when a caller asks for a cluster
// that is neither cached nor known through datasource discovery.
var ErrSchemaClusterNotConfigured = errors.New("clickhouse schema cluster is not configured")

// FetchTablesInCluster fetches a lightweight live table listing for one cluster.
// The returned schemas intentionally contain only table identity; callers should
// use FetchTableInCluster for full details.
func (c *clickhouseSchemaClient) FetchTablesInCluster(
	ctx context.Context,
	clusterName, database string,
) (*ClusterTables, error) {
	datasourceName, ok := c.datasourceForCluster(clusterName)
	if !ok {
		return nil, ErrSchemaClusterNotConfigured
	}

	var (
		discovered []discoveredTable
		err        error
	)

	if strings.TrimSpace(database) != "" {
		names, listErr := c.fetchTablesFromDatabase(ctx, datasourceName, database)
		if listErr != nil {
			return nil, listErr
		}

		discovered = make([]discoveredTable, 0, len(names))
		for _, name := range names {
			discovered = append(discovered, discoveredTable{Name: name, Database: database})
		}
	} else {
		discovered, err = c.fetchTableList(ctx, datasourceName)
		if err != nil {
			return nil, err
		}
	}

	cluster := &ClusterTables{
		ClusterName: clusterName,
		Tables:      make(map[string]*TableSchema, len(discovered)),
		LastUpdated: time.Now().UTC(),
	}

	for _, dt := range discovered {
		cluster.Tables[tableKey(dt.Database, dt.Name)] = &TableSchema{
			Database: dt.Database,
			Name:     dt.Name,
		}
	}

	return cluster, nil
}

// FetchTableInCluster fetches one exact table schema on demand. This keeps the
// schema resource useful while the background all-table cache is still warming.
func (c *clickhouseSchemaClient) FetchTableInCluster(ctx context.Context, clusterName, database, tableName string) (*TableSchema, error) {
	if schema, ok := c.GetTableInCluster(clusterName, database, tableName); ok {
		return schema, nil
	}

	datasourceName, ok := c.datasourceForCluster(clusterName)
	if !ok {
		return nil, ErrSchemaClusterNotConfigured
	}

	schema, err := c.fetchTableSchema(ctx, datasourceName, tableName, database)
	if err != nil {
		return nil, err
	}

	schema.Database = database

	if shouldFetchLocalBackingKeys(schema) {
		localSchema, err := c.fetchLocalBackingTableSchema(ctx, datasourceName, discoveredTable{
			Name:     tableName,
			Database: database,
		})
		if err != nil {
			c.log.WithError(err).WithFields(logrus.Fields{
				"cluster":  clusterName,
				"database": database,
				"table":    tableName,
			}).Debug("Failed to fetch live local backing table key clauses")
		} else {
			copyMissingKeyClauses(schema, localSchema)
		}
	}

	return schema, nil
}

func (c *clickhouseSchemaClient) datasourceForCluster(clusterName string) (string, bool) {
	c.dsMu.RLock()
	defer c.dsMu.RUnlock()

	datasourceName, ok := c.datasources[clusterName]

	return datasourceName, ok
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
		case <-c.refreshNow:
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
	datasources := c.snapshotDatasources()

	if len(datasources) == 0 {
		// Drop any stale cached schemas — the proxy no longer reports any
		// ClickHouse clusters, so callers should see an empty view rather than
		// last-known-good data that points at clusters that don't exist.
		c.mu.Lock()
		hadStale := len(c.clusters) > 0
		c.clusters = make(map[string]*ClusterTables, 0)
		c.mu.Unlock()

		if hadStale {
			c.log.Info("ClickHouse datasource list emptied; cleared cached schemas")
		} else {
			c.log.Debug("No ClickHouse datasources available for schema discovery")
		}

		return nil
	}

	newClusters := make(map[string]*ClusterTables, len(datasources))

	for clusterName, datasourceName := range datasources {
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
	discovered, err := c.fetchTableList(ctx, datasourceName)
	if err != nil {
		return nil, fmt.Errorf("fetching table list: %w", err)
	}

	c.log.WithFields(logrus.Fields{
		"cluster": clusterName,
		"tables":  len(discovered),
	}).Info("Discovered tables for cluster, fetching schemas")

	clusterTables := &ClusterTables{
		ClusterName: clusterName,
		Tables:      make(map[string]*TableSchema, len(discovered)),
		LastUpdated: time.Now(),
	}

	sem := make(chan struct{}, schemaQueryConcurrency)

	var wg sync.WaitGroup

	var mu sync.Mutex

	for _, dt := range discovered {
		wg.Add(1)

		go func(dt discoveredTable) {
			defer wg.Done()

			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			schema, err := c.fetchTableSchema(ctx, datasourceName, dt.Name, dt.Database)
			if err != nil {
				c.log.WithError(err).WithFields(logrus.Fields{
					"database": dt.Database,
					"table":    dt.Name,
				}).Debug("Failed to fetch table schema")

				return
			}

			schema.Database = dt.Database

			if shouldFetchLocalBackingKeys(schema) {
				localSchema, err := c.fetchLocalBackingTableSchema(ctx, datasourceName, dt)
				if err != nil {
					c.log.WithError(err).WithFields(logrus.Fields{
						"database": dt.Database,
						"table":    dt.Name,
					}).Debug("Failed to fetch local backing table key clauses")
				} else {
					copyMissingKeyClauses(schema, localSchema)
				}
			}

			mu.Lock()
			clusterTables.Tables[tableKey(dt.Database, dt.Name)] = schema
			mu.Unlock()
		}(dt)
	}

	wg.Wait()

	return clusterTables, nil
}

func shouldFetchLocalBackingKeys(schema *TableSchema) bool {
	if schema == nil || !strings.EqualFold(schema.Engine, "Distributed") {
		return false
	}

	return schema.PartitionBy == "" || schema.OrderBy == "" || schema.PrimaryKey == ""
}

func (c *clickhouseSchemaClient) fetchLocalBackingTableSchema(
	ctx context.Context,
	datasourceName string,
	dt discoveredTable,
) (*TableSchema, error) {
	localName, ok := localBackingTableName(dt.Name)
	if !ok {
		return nil, fmt.Errorf("table %q has no local backing-table convention", dt.Name)
	}

	return c.fetchTableSchema(ctx, datasourceName, localName, dt.Database)
}

func localBackingTableName(tableName string) (string, bool) {
	if tableName == "" || strings.HasSuffix(tableName, "_local") {
		return "", false
	}

	return tableName + "_local", true
}

func copyMissingKeyClauses(schema, localSchema *TableSchema) {
	if schema == nil || localSchema == nil {
		return
	}

	if schema.PartitionBy == "" {
		schema.PartitionBy = localSchema.PartitionBy
	}

	if schema.OrderBy == "" {
		schema.OrderBy = localSchema.OrderBy
	}

	if schema.PrimaryKey == "" {
		schema.PrimaryKey = localSchema.PrimaryKey
	}
}

type clickhouseJSONMeta struct {
	Name string `json:"name"`
}

type clickhouseJSONResponse struct {
	Meta []clickhouseJSONMeta `json:"meta"`
	Data []map[string]any     `json:"data"`
	Rows int                  `json:"rows"`
	Err  *clickhouseJSONError `json:"error,omitempty"`
}

type clickhouseJSONError struct {
	Message string `json:"message"`
	Code    int    `json:"code"`
}

func pickColumn(meta []clickhouseJSONMeta, preferred string) string {
	if preferred != "" {
		for _, m := range meta {
			if m.Name == preferred {
				return m.Name
			}
		}
	}

	if len(meta) > 0 {
		return meta[0].Name
	}

	return ""
}

func asString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}

func (c *clickhouseSchemaClient) queryJSON(ctx context.Context, datasourceName, sql string) (*clickhouseJSONResponse, error) {
	if datasourceName == "" {
		return nil, fmt.Errorf("datasource name is required")
	}

	if c.proxySvc == nil {
		return nil, fmt.Errorf("proxy service is required")
	}

	reqCtx, cancel := context.WithTimeout(ctx, c.cfg.QueryTimeout)
	defer cancel()

	params := url.Values{"default_format": {"JSON"}}

	body, err := c.proxySvc.ClickHouseQuery(reqCtx, datasourceName, sql, params)
	if err != nil {
		return nil, err
	}

	var result clickhouseJSONResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	if result.Err != nil {
		return nil, fmt.Errorf("query error (%d): %s", result.Err.Code, result.Err.Message)
	}

	return &result, nil
}

// fetchTableList discovers tables for a datasource by merging the default
// database (SHOW TABLES) with every other non-system database. A cluster can
// hold data in both at once — e.g. a populated default database plus
// per-dataset databases — so short-circuiting on a non-empty default database
// would hide the others. Each source failing alone is tolerated; both
// failing is an error.
func (c *clickhouseSchemaClient) fetchTableList(ctx context.Context, datasourceName string) ([]discoveredTable, error) {
	defaultTables, defaultErr := c.fetchTableListDefault(ctx, datasourceName)
	if defaultErr != nil {
		c.log.WithError(defaultErr).WithField("datasource", datasourceName).
			Warn("Default-database table discovery failed")
	}

	databaseTables, databasesErr := c.fetchTableListFromSystemTables(ctx, datasourceName)
	if databasesErr != nil {
		c.log.WithError(databasesErr).WithField("datasource", datasourceName).
			Warn("Per-database table discovery failed")
	}

	if defaultErr != nil && databasesErr != nil {
		return nil, fmt.Errorf("discovering tables: %w", errors.Join(defaultErr, databasesErr))
	}

	seen := make(map[string]bool, len(defaultTables)+len(databaseTables))
	merged := make([]discoveredTable, 0, len(defaultTables)+len(databaseTables))

	for _, t := range append(defaultTables, databaseTables...) {
		key := t.Database + "\x00" + t.Name
		if seen[key] {
			continue
		}

		seen[key] = true

		merged = append(merged, t)
	}

	return merged, nil
}

// fetchTableListDefault fetches tables from the cluster's default database via
// SHOW TABLES, resolving the database name via currentDatabase() so each entry
// carries an explicit Database.
func (c *clickhouseSchemaClient) fetchTableListDefault(ctx context.Context, datasourceName string) ([]discoveredTable, error) {
	dbResult, err := c.queryJSON(ctx, datasourceName, "SELECT currentDatabase() AS db")
	if err != nil {
		return nil, fmt.Errorf("resolving current database: %w", err)
	}

	currentDB := ""
	if len(dbResult.Data) > 0 {
		col := pickColumn(dbResult.Meta, "db")
		currentDB = strings.TrimSpace(asString(dbResult.Data[0][col]))
	}

	result, err := c.queryJSON(ctx, datasourceName, "SHOW TABLES")
	if err != nil {
		return nil, fmt.Errorf("executing SHOW TABLES: %w", err)
	}

	column := pickColumn(result.Meta, "name")
	if column == "" {
		return nil, fmt.Errorf("SHOW TABLES response missing columns")
	}

	tables := make([]discoveredTable, 0, len(result.Data))
	for _, row := range result.Data {
		tableName := strings.TrimSpace(asString(row[column]))
		if tableName == "" {
			continue
		}

		if strings.HasSuffix(tableName, "_local") {
			continue
		}

		tables = append(tables, discoveredTable{Name: tableName, Database: currentDB})
	}

	return tables, nil
}

// systemDatabaseBlacklist contains databases to skip during per-network discovery.
var systemDatabaseBlacklist = map[string]bool{
	"system":                         true,
	"information_schema":             true,
	"INFORMATION_SCHEMA":             true,
	"default":                        true,
	"_temporary_and_external_tables": true,
}

// fetchTableListFromSystemTables emits one discoveredTable per (database, table)
// for every non-system database. Used when the cluster has no default database.
func (c *clickhouseSchemaClient) fetchTableListFromSystemTables(ctx context.Context, datasourceName string) ([]discoveredTable, error) {
	databases, err := c.fetchDatabases(ctx, datasourceName)
	if err != nil {
		return nil, fmt.Errorf("discovering databases: %w", err)
	}

	if len(databases) == 0 {
		return nil, nil
	}

	tables := make([]discoveredTable, 0, 256)

	for _, db := range databases {
		dbTables, err := c.fetchTablesFromDatabase(ctx, datasourceName, db)
		if err != nil {
			c.log.WithError(err).WithField("database", db).Debug("Failed to list tables from database")

			continue
		}

		for _, name := range dbTables {
			tables = append(tables, discoveredTable{Name: name, Database: db})
		}
	}

	return tables, nil
}

// fetchDatabases returns non-system database names from a ClickHouse cluster.
func (c *clickhouseSchemaClient) fetchDatabases(ctx context.Context, datasourceName string) ([]string, error) {
	result, err := c.queryJSON(ctx, datasourceName, "SHOW DATABASES")
	if err != nil {
		return nil, fmt.Errorf("executing SHOW DATABASES: %w", err)
	}

	column := pickColumn(result.Meta, "name")
	if column == "" {
		return nil, fmt.Errorf("SHOW DATABASES response missing columns")
	}

	databases := make([]string, 0, len(result.Data))
	for _, row := range result.Data {
		db := strings.TrimSpace(asString(row[column]))
		if db == "" || systemDatabaseBlacklist[db] {
			continue
		}

		databases = append(databases, db)
	}

	return databases, nil
}

// fetchTablesFromDatabase lists tables in a specific database.
func (c *clickhouseSchemaClient) fetchTablesFromDatabase(ctx context.Context, datasourceName, database string) ([]string, error) {
	if err := validateIdentifier(database); err != nil {
		return nil, fmt.Errorf("validating database name: %w", err)
	}

	query := fmt.Sprintf("SHOW TABLES FROM `%s`", database)

	result, err := c.queryJSON(ctx, datasourceName, query)
	if err != nil {
		return nil, fmt.Errorf("executing SHOW TABLES FROM %s: %w", database, err)
	}

	column := pickColumn(result.Meta, "name")
	if column == "" {
		return nil, fmt.Errorf("SHOW TABLES FROM %s response missing columns", database)
	}

	tables := make([]string, 0, len(result.Data))
	for _, row := range result.Data {
		name := strings.TrimSpace(asString(row[column]))
		if name == "" {
			continue
		}

		if strings.HasSuffix(name, "_local") {
			continue
		}

		tables = append(tables, name)
	}

	return tables, nil
}

// validateIdentifier validates a ClickHouse table/column identifier to prevent SQL injection.
func validateIdentifier(name string) error {
	if !validIdentifier.MatchString(name) {
		return fmt.Errorf("invalid identifier %q: must match [A-Za-z_][A-Za-z0-9_]*", name)
	}

	return nil
}

// fetchTableSchema fetches the schema for a specific table.
// When database is non-empty, the query is qualified as `database`.`table`.
func (c *clickhouseSchemaClient) fetchTableSchema(
	ctx context.Context,
	datasourceName string,
	tableName string,
	database string,
) (*TableSchema, error) {
	if err := validateIdentifier(tableName); err != nil {
		return nil, fmt.Errorf("validating table name: %w", err)
	}

	query := fmt.Sprintf("SHOW CREATE TABLE `%s`", tableName)
	if database != "" {
		if err := validateIdentifier(database); err != nil {
			return nil, fmt.Errorf("validating database name: %w", err)
		}

		query = fmt.Sprintf("SHOW CREATE TABLE `%s`.`%s`", database, tableName)
	}

	result, err := c.queryJSON(ctx, datasourceName, query)
	if err != nil {
		return nil, fmt.Errorf("executing SHOW CREATE TABLE: %w", err)
	}

	if len(result.Data) == 0 {
		return nil, fmt.Errorf("empty CREATE TABLE statement for table %s", tableName)
	}

	column := pickColumn(result.Meta, "")
	if column == "" {
		return nil, fmt.Errorf("SHOW CREATE TABLE response missing columns")
	}

	createStmt := strings.TrimSpace(asString(result.Data[0][column]))

	if createStmt == "" {
		return nil, fmt.Errorf("empty CREATE TABLE statement for table %s", tableName)
	}

	return parseCreateTable(tableName, createStmt)
}

// parseCreateTable parses SHOW CREATE TABLE output to extract schema info.
func parseCreateTable(tableName, createStmt string) (*TableSchema, error) {
	schema := &TableSchema{
		Name:            tableName,
		CreateStatement: createStmt,
		Columns:         make([]TableColumn, 0, 32),
	}

	// Extract columns from the CREATE TABLE statement.
	// Find the content between the first ( and the matching ).
	startIdx := strings.Index(createStmt, "(")
	if startIdx == -1 {
		return schema, nil
	}

	// Find matching closing parenthesis.
	depth := 0
	endIdx := -1

outerLoop:
	for i := startIdx; i < len(createStmt); i++ {
		switch createStmt[i] {
		case '(':
			depth++
		case ')':
			depth--

			if depth == 0 {
				endIdx = i

				break outerLoop
			}
		}
	}

	if endIdx == -1 {
		return schema, nil
	}

	columnsSection := createStmt[startIdx+1 : endIdx]

	// Extract engine and table comment from the suffix after the column definitions.
	// This avoids matching column-level COMMENT clauses inside the parentheses.
	suffix := createStmt[endIdx:]

	if matches := enginePattern.FindStringSubmatch(suffix); len(matches) > 1 {
		schema.Engine = matches[1]
	}

	if matches := tableCommentPattern.FindStringSubmatch(suffix); len(matches) > 1 {
		schema.Comment = matches[1]
	}

	schema.PartitionBy = extractCreateClause(suffix, "PARTITION BY")
	schema.OrderBy = extractCreateClause(suffix, "ORDER BY")
	schema.PrimaryKey = extractCreateClause(suffix, "PRIMARY KEY")

	// Parse each column definition.
	// Column format: `name` Type [DEFAULT expr] [CODEC(...)] [COMMENT 'comment'].

	lines := strings.Split(columnsSection, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "INDEX") || strings.HasPrefix(line, "PROJECTION") {
			continue
		}

		colMatches := columnPattern.FindStringSubmatch(line)
		if len(colMatches) < 3 {
			continue
		}

		col := TableColumn{
			Name: colMatches[1],
			Type: strings.TrimSpace(colMatches[2]),
		}

		// Clean up the type - remove trailing commas and other clauses.
		col.Type = cleanColumnType(col.Type)

		// Extract comment.
		if commentMatches := columnCommentPattern.FindStringSubmatch(line); len(commentMatches) > 1 {
			col.Comment = commentMatches[1]
		}

		// Extract default.
		if defaultMatches := defaultPattern.FindStringSubmatch(line); len(defaultMatches) > 2 {
			col.DefaultType = defaultMatches[1]
			col.DefaultValue = strings.TrimSpace(defaultMatches[2])
		}

		schema.Columns = append(schema.Columns, col)
	}

	return schema, nil
}

var createClauseStopKeywords = []string{
	"PARTITION BY",
	"PRIMARY KEY",
	"ORDER BY",
	"SAMPLE BY",
	"TTL",
	"SETTINGS",
	"COMMENT",
}

func extractCreateClause(s, keyword string) string {
	_, keywordEnd, ok := findTopLevelKeyword(s, keyword, 0)
	if !ok {
		return ""
	}

	start := skipSpaces(s, keywordEnd)
	end := len(s)

	for _, stop := range createClauseStopKeywords {
		if strings.EqualFold(stop, keyword) {
			continue
		}

		stopStart, _, found := findTopLevelKeyword(s, stop, start)
		if found && stopStart < end {
			end = stopStart
		}
	}

	expr := strings.TrimSpace(s[start:end])
	expr = strings.TrimSuffix(expr, ";")
	expr = strings.TrimSuffix(strings.TrimSpace(expr), ",")

	return strings.TrimSpace(expr)
}

func findTopLevelKeyword(s, keyword string, from int) (int, int, bool) {
	depth := 0
	quoted := byte(0)

	for i := max(from, 0); i < len(s); i++ {
		ch := s[i]

		if quoted != 0 {
			if ch == '\\' && i+1 < len(s) {
				i++

				continue
			}

			if ch == quoted {
				quoted = 0
			}

			continue
		}

		switch ch {
		case '\'', '"', '`':
			quoted = ch
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				if end, ok := matchKeywordAt(s, i, keyword); ok {
					return i, end, true
				}
			}
		}
	}

	return 0, 0, false
}

func matchKeywordAt(s string, pos int, keyword string) (int, bool) {
	if pos > 0 && isIdentifierByte(s[pos-1]) {
		return 0, false
	}

	parts := strings.Fields(keyword)
	cur := pos

	for i, part := range parts {
		if i > 0 {
			if cur >= len(s) || !isSpaceByte(s[cur]) {
				return 0, false
			}

			cur = skipSpaces(s, cur)
		}

		if cur+len(part) > len(s) || !strings.EqualFold(s[cur:cur+len(part)], part) {
			return 0, false
		}

		cur += len(part)
	}

	if cur < len(s) && isIdentifierByte(s[cur]) {
		return 0, false
	}

	return cur, true
}

func skipSpaces(s string, pos int) int {
	for pos < len(s) && isSpaceByte(s[pos]) {
		pos++
	}

	return pos
}

func isSpaceByte(ch byte) bool {
	return ch == ' ' || ch == '\n' || ch == '\r' || ch == '\t'
}

func isIdentifierByte(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') ||
		(ch >= 'A' && ch <= 'Z') ||
		(ch >= '0' && ch <= '9') ||
		ch == '_'
}

// cleanColumnType removes trailing clauses from the column type.
func cleanColumnType(colType string) string {
	// Remove everything after DEFAULT, CODEC, COMMENT.
	for _, keyword := range []string{" DEFAULT", " CODEC", " COMMENT", " MATERIALIZED", " ALIAS"} {
		if idx := strings.Index(strings.ToUpper(colType), keyword); idx != -1 {
			colType = colType[:idx]
		}
	}

	// Remove trailing comma.
	colType = strings.TrimSuffix(strings.TrimSpace(colType), ",")

	return colType
}
