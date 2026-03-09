package clickhouse

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/mcp/pkg/proxy"
	"github.com/ethpandaops/mcp/pkg/proxy/handlers"
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
	// Start initializes the client and fetches initial schema data.
	Start(ctx context.Context) error
	// Stop stops background refresh.
	Stop() error
	// WaitForReady blocks until the initial schema fetch completes or ctx is cancelled.
	WaitForReady(ctx context.Context) error
	// GetAllTables returns all tables across all clusters.
	GetAllTables() map[string]*ClusterTables
	// GetTable returns schema for a specific table (searches all clusters).
	GetTable(tableName string) (*TableSchema, string, bool)
}

// Compile-time interface compliance check.
var _ ClickHouseSchemaClient = (*clickhouseSchemaClient)(nil)

type clickhouseSchemaClient struct {
	log      logrus.FieldLogger
	cfg      ClickHouseSchemaConfig
	proxySvc proxy.Service

	mu          sync.RWMutex
	clusters    map[string]*ClusterTables
	datasources map[string]string // cluster name -> datasource name

	done  chan struct{}
	ready chan struct{} // closed when initial fetch completes
	wg    sync.WaitGroup

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
		httpClient:  &http.Client{},
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
	if c.proxySvc == nil {
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

	token := c.proxySvc.RegisterToken("clickhouse-schema")
	defer c.proxySvc.RevokeToken("clickhouse-schema")

	if token == "" {
		c.log.Warn("Proxy token is empty; schema discovery requests may fail if auth is required")
	}

	newClusters := make(map[string]*ClusterTables, len(c.datasources))

	for clusterName, datasourceName := range c.datasources {
		tables, err := c.discoverClusterSchema(ctx, clusterName, datasourceName, token)
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
	token string,
) (*ClusterTables, error) {
	// Get table list.
	tables, err := c.fetchTableList(ctx, datasourceName, token)
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

			schema, err := c.fetchTableSchema(ctx, datasourceName, token, name)
			if err != nil {
				c.log.WithError(err).WithField("table", name).Debug("Failed to fetch table schema")

				return
			}

			// Get networks if table has meta_network_name column.
			if schema.HasNetworkCol {
				networks, err := c.fetchTableNetworks(ctx, datasourceName, token, name)
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

func (c *clickhouseSchemaClient) queryJSON(ctx context.Context, datasourceName, token, sql string) (*clickhouseJSONResponse, error) {
	if datasourceName == "" {
		return nil, fmt.Errorf("datasource name is required")
	}

	baseURL := strings.TrimRight(c.proxySvc.URL(), "/")
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
	if token != "" {
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
func (c *clickhouseSchemaClient) fetchTableList(ctx context.Context, datasourceName, token string) ([]string, error) {
	result, err := c.queryJSON(ctx, datasourceName, token, "SHOW TABLES")
	if err != nil {
		return nil, fmt.Errorf("executing SHOW TABLES: %w", err)
	}

	column := pickColumn(result.Meta, "name")
	if column == "" {
		return nil, fmt.Errorf("SHOW TABLES response missing columns")
	}

	tables := make([]string, 0, len(result.Data))
	for _, row := range result.Data {
		tableName := strings.TrimSpace(asString(row[column]))
		if tableName == "" {
			continue
		}

		// Skip tables with _local suffix (internal ClickHouse distributed table shards).
		if strings.HasSuffix(tableName, "_local") {
			continue
		}

		tables = append(tables, tableName)
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
func (c *clickhouseSchemaClient) fetchTableSchema(
	ctx context.Context,
	datasourceName string,
	token string,
	tableName string,
) (*TableSchema, error) {
	if err := validateIdentifier(tableName); err != nil {
		return nil, fmt.Errorf("validating table name: %w", err)
	}

	query := fmt.Sprintf("SHOW CREATE TABLE `%s`", tableName)

	result, err := c.queryJSON(ctx, datasourceName, token, query)
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

// fetchTableNetworks fetches distinct networks available in a table.
func (c *clickhouseSchemaClient) fetchTableNetworks(
	ctx context.Context,
	datasourceName string,
	token string,
	tableName string,
) ([]string, error) {
	if err := validateIdentifier(tableName); err != nil {
		return nil, fmt.Errorf("validating table name: %w", err)
	}

	query := fmt.Sprintf(
		"SELECT DISTINCT meta_network_name FROM `%s` WHERE meta_network_name IS NOT NULL AND meta_network_name != '' LIMIT 1000",
		tableName,
	)

	result, err := c.queryJSON(ctx, datasourceName, token, query)
	if err != nil {
		return nil, fmt.Errorf("executing network query: %w", err)
	}

	column := pickColumn(result.Meta, "meta_network_name")
	if column == "" {
		return nil, fmt.Errorf("network query response missing columns")
	}

	networks := make([]string, 0, len(result.Data))
	for _, row := range result.Data {
		network := strings.TrimSpace(asString(row[column]))
		if network != "" {
			networks = append(networks, network)
		}
	}

	sort.Strings(networks)

	return networks, nil
}

// parseCreateTable parses SHOW CREATE TABLE output to extract schema info.
func parseCreateTable(tableName, createStmt string) (*TableSchema, error) {
	schema := &TableSchema{
		Name:            tableName,
		CreateStatement: createStmt,
		Columns:         make([]TableColumn, 0, 32),
	}

	// Extract engine.
	if matches := enginePattern.FindStringSubmatch(createStmt); len(matches) > 1 {
		schema.Engine = matches[1]
	}

	// Extract table comment.
	if matches := tableCommentPattern.FindStringSubmatch(createStmt); len(matches) > 1 {
		schema.Comment = matches[1]
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
