package clickhouse

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/proxy"
	"github.com/ethpandaops/panda/pkg/proxy/handlers"
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

// ClickHouseSchemaConfig holds configuration for schema discovery.
type ClickHouseSchemaConfig struct {
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
	Columns         []TableColumn `json:"columns"`
	HasNetworkCol   bool          `json:"has_network_column"`
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

// ClickHouseSchemaClient fetches and caches ClickHouse schema information.
type ClickHouseSchemaClient interface {
	Start(ctx context.Context) error
	Stop() error
	WaitForReady(ctx context.Context) error
	GetAllTables() map[string]*ClusterTables
	GetClusterTables(clusterName string) (*ClusterTables, bool)
	GetTableInCluster(clusterName, database, tableName string) (*TableSchema, bool)
	UpdateDatasources(datasources []SchemaDiscoveryDatasource)
}

// Compile-time interface compliance check.
var _ ClickHouseSchemaClient = (*clickhouseSchemaClient)(nil)

type clickhouseSchemaClient struct {
	log      logrus.FieldLogger
	cfg      ClickHouseSchemaConfig
	proxySvc proxy.Service

	mu       sync.RWMutex
	clusters map[string]*ClusterTables

	dsMu        sync.RWMutex
	datasources map[string]string // cluster name -> datasource name

	done       chan struct{}
	ready      chan struct{} // closed when initial fetch completes
	refreshNow chan struct{} // capacity 1; coalesces on-demand refresh signals
	wg         sync.WaitGroup

	httpClient *http.Client
}

// NewClickHouseSchemaClient creates a new schema discovery client.
func NewClickHouseSchemaClient(
	log logrus.FieldLogger,
	cfg ClickHouseSchemaConfig,
	proxySvc proxy.Service,
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
		proxySvc:    proxySvc,
		clusters:    make(map[string]*ClusterTables, 2),
		datasources: make(map[string]string, 2),
		done:        make(chan struct{}),
		ready:       make(chan struct{}),
		refreshNow:  make(chan struct{}, 1),
		httpClient:  &http.Client{},
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

// WaitForReady blocks until the initial schema fetch completes or ctx is cancelled.
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
		datasourceProxy, err := c.proxyForDatasource(datasourceName)
		if err != nil {
			c.log.WithError(err).WithFields(logrus.Fields{
				"cluster":    clusterName,
				"datasource": datasourceName,
			}).Warn("Failed to resolve ClickHouse schema datasource owner")

			continue
		}

		tokenID := "clickhouse-schema-" + datasourceName
		token := datasourceProxy.RegisterToken(tokenID)
		if token == "" {
			c.log.WithField("datasource", datasourceName).Warn("Proxy token is empty; schema discovery requests may fail if auth is required")
		}

		tables, err := c.discoverClusterSchema(ctx, clusterName, datasourceName, datasourceProxy, token)
		datasourceProxy.RevokeToken(tokenID)
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

func (c *clickhouseSchemaClient) proxyForDatasource(datasourceName string) (proxy.Service, error) {
	if c.proxySvc == nil {
		return nil, fmt.Errorf("proxy service is required for schema discovery")
	}

	if router, ok := c.proxySvc.(proxy.Router); ok {
		client, found := router.ClientForDatasource("clickhouse", datasourceName)
		if !found {
			return nil, fmt.Errorf("clickhouse datasource %q not found", datasourceName)
		}

		return client, nil
	}

	return c.proxySvc, nil
}

// discoverClusterSchema discovers schema for a single cluster.
func (c *clickhouseSchemaClient) discoverClusterSchema(
	ctx context.Context,
	clusterName string,
	datasourceName string,
	proxySvc proxy.Service,
	token string,
) (*ClusterTables, error) {
	discovered, err := c.fetchTableList(ctx, proxySvc, datasourceName, token)
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

			schema, err := c.fetchTableSchema(ctx, proxySvc, datasourceName, token, dt.Name, dt.Database)
			if err != nil {
				c.log.WithError(err).WithFields(logrus.Fields{
					"database": dt.Database,
					"table":    dt.Name,
				}).Debug("Failed to fetch table schema")

				return
			}

			schema.Database = dt.Database

			mu.Lock()
			clusterTables.Tables[tableKey(dt.Database, dt.Name)] = schema
			mu.Unlock()
		}(dt)
	}

	wg.Wait()

	return clusterTables, nil
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

func (c *clickhouseSchemaClient) queryJSON(
	ctx context.Context,
	proxySvc proxy.Service,
	datasourceName string,
	token string,
	sql string,
) (*clickhouseJSONResponse, error) {
	if datasourceName == "" {
		return nil, fmt.Errorf("datasource name is required")
	}

	if proxySvc == nil {
		return nil, fmt.Errorf("proxy service is required")
	}

	baseURL := strings.TrimRight(proxySvc.URL(), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("proxy URL is empty")
	}

	reqCtx, cancel := context.WithTimeout(ctx, c.cfg.QueryTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, baseURL+"/clickhouse/", strings.NewReader(sql))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set(handlers.DatasourceHeader, datasourceName)
	if token != "" && token != "none" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "text/plain")

	q := req.URL.Query()
	q.Set("default_format", "JSON")
	req.URL.RawQuery = q.Encode()

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing query: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("query failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result clickhouseJSONResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	if result.Err != nil {
		return nil, fmt.Errorf("query error (%d): %s", result.Err.Code, result.Err.Message)
	}

	return &result, nil
}

// fetchTableList fetches the list of tables from a ClickHouse datasource.
// First tries SHOW TABLES (works for clusters with a default database like clickhouse-raw).
// If that returns 0 rows, falls back to querying system.tables to discover
// tables across per-network databases (like clickhouse-refined).
func (c *clickhouseSchemaClient) fetchTableList(
	ctx context.Context,
	proxySvc proxy.Service,
	datasourceName string,
	token string,
) ([]discoveredTable, error) {
	tables, err := c.fetchTableListDefault(ctx, proxySvc, datasourceName, token)
	if err != nil {
		return nil, err
	}

	if len(tables) > 0 {
		return tables, nil
	}

	// Default database is empty — try per-network-database discovery.
	c.log.WithField("datasource", datasourceName).Info("SHOW TABLES returned 0 rows, trying per-database discovery fallback")

	return c.fetchTableListFromSystemTables(ctx, proxySvc, datasourceName, token)
}

// fetchTableListDefault fetches tables from the cluster's default database via
// SHOW TABLES, resolving the database name via currentDatabase() so each entry
// carries an explicit Database.
func (c *clickhouseSchemaClient) fetchTableListDefault(
	ctx context.Context,
	proxySvc proxy.Service,
	datasourceName string,
	token string,
) ([]discoveredTable, error) {
	dbResult, err := c.queryJSON(ctx, proxySvc, datasourceName, token, "SELECT currentDatabase() AS db")
	if err != nil {
		return nil, fmt.Errorf("resolving current database: %w", err)
	}

	currentDB := ""
	if len(dbResult.Data) > 0 {
		col := pickColumn(dbResult.Meta, "db")
		currentDB = strings.TrimSpace(asString(dbResult.Data[0][col]))
	}

	result, err := c.queryJSON(ctx, proxySvc, datasourceName, token, "SHOW TABLES")
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
// for every non-system database. Used when the cluster's default database is
// empty (clickhouse-refined, observability tier-scoped clusters).
func (c *clickhouseSchemaClient) fetchTableListFromSystemTables(
	ctx context.Context,
	proxySvc proxy.Service,
	datasourceName string,
	token string,
) ([]discoveredTable, error) {
	databases, err := c.fetchDatabases(ctx, proxySvc, datasourceName, token)
	if err != nil {
		return nil, fmt.Errorf("discovering databases: %w", err)
	}

	if len(databases) == 0 {
		return nil, nil
	}

	tables := make([]discoveredTable, 0, 256)

	for _, db := range databases {
		dbTables, err := c.fetchTablesFromDatabase(ctx, proxySvc, datasourceName, token, db)
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
func (c *clickhouseSchemaClient) fetchDatabases(
	ctx context.Context,
	proxySvc proxy.Service,
	datasourceName string,
	token string,
) ([]string, error) {
	result, err := c.queryJSON(ctx, proxySvc, datasourceName, token, "SHOW DATABASES")
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
func (c *clickhouseSchemaClient) fetchTablesFromDatabase(
	ctx context.Context,
	proxySvc proxy.Service,
	datasourceName string,
	token string,
	database string,
) ([]string, error) {
	if err := validateIdentifier(database); err != nil {
		return nil, fmt.Errorf("validating database name: %w", err)
	}

	query := fmt.Sprintf("SHOW TABLES FROM `%s`", database)

	result, err := c.queryJSON(ctx, proxySvc, datasourceName, token, query)
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
	proxySvc proxy.Service,
	datasourceName string,
	token string,
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

	result, err := c.queryJSON(ctx, proxySvc, datasourceName, token, query)
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

		// Check for meta_network_name column.
		if col.Name == "meta_network_name" {
			schema.HasNetworkCol = true
		}

		schema.Columns = append(schema.Columns, col)
	}

	return schema, nil
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
