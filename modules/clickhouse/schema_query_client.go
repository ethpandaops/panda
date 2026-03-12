package clickhouse

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/ethpandaops/panda/pkg/proxy"
)

type clickhouseSchemaQueryClient struct {
	proxySvc     proxy.ClickHouseSchemaAccess
	httpClient   *http.Client
	queryTimeout time.Duration
	parser       clickhouseDDLParser
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

func newClickhouseSchemaQueryClient(
	proxySvc proxy.ClickHouseSchemaAccess,
	httpClient *http.Client,
	queryTimeout time.Duration,
) clickhouseSchemaQueryClient {
	return clickhouseSchemaQueryClient{
		proxySvc:     proxySvc,
		httpClient:   httpClient,
		queryTimeout: queryTimeout,
		parser:       clickhouseDDLParser{},
	}
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

func (c clickhouseSchemaQueryClient) queryJSON(
	ctx context.Context,
	datasourceName, sql string,
) (*clickhouseJSONResponse, error) {
	if datasourceName == "" {
		return nil, fmt.Errorf("datasource name is required")
	}

	baseURL := strings.TrimRight(c.proxySvc.URL(), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("proxy URL is empty")
	}

	reqCtx, cancel := context.WithTimeout(ctx, c.queryTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, baseURL+"/clickhouse/", strings.NewReader(sql))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set(proxy.DatasourceHeader, datasourceName)
	if err := c.proxySvc.AuthorizeRequest(req); err != nil {
		return nil, fmt.Errorf("authorizing request: %w", err)
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

func (c clickhouseSchemaQueryClient) fetchTableList(ctx context.Context, datasourceName string) ([]string, error) {
	result, err := c.queryJSON(ctx, datasourceName, "SHOW TABLES")
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
		if tableName == "" || strings.HasSuffix(tableName, "_local") {
			continue
		}

		tables = append(tables, tableName)
	}

	return tables, nil
}

func (c clickhouseSchemaQueryClient) fetchTableSchema(
	ctx context.Context,
	datasourceName, tableName string,
) (*TableSchema, error) {
	if err := validateIdentifier(tableName); err != nil {
		return nil, fmt.Errorf("validating table name: %w", err)
	}

	result, err := c.queryJSON(ctx, datasourceName, fmt.Sprintf("SHOW CREATE TABLE `%s`", tableName))
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

	return c.parser.ParseTable(tableName, createStmt)
}

func (c clickhouseSchemaQueryClient) fetchTableNetworks(
	ctx context.Context,
	datasourceName, tableName string,
) ([]string, error) {
	if err := validateIdentifier(tableName); err != nil {
		return nil, fmt.Errorf("validating table name: %w", err)
	}

	query := fmt.Sprintf(
		"SELECT DISTINCT meta_network_name FROM `%s` WHERE meta_network_name IS NOT NULL AND meta_network_name != '' LIMIT 1000",
		tableName,
	)

	result, err := c.queryJSON(ctx, datasourceName, query)
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
