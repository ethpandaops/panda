package clickhouse

import (
	"context"
	"io"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/proxy"
	"github.com/ethpandaops/panda/pkg/types"
)

func TestSchemaRefreshRoutesDatasourceToOwningProxy(t *testing.T) {
	t.Parallel()

	hosted := &schemaProxyClient{
		url:        "https://hosted.proxy",
		token:      "hosted-token",
		clickhouse: []types.DatasourceInfo{{Name: "xatu"}},
	}
	local := &schemaProxyClient{
		url:        "https://local.proxy",
		token:      "local-token",
		clickhouse: []types.DatasourceInfo{{Name: "local-kurtosis"}},
	}

	log := logrus.New()
	log.SetOutput(io.Discard)

	client := NewSchemaClient(
		log,
		SchemaConfig{
			QueryTimeout: time.Second,
			Datasources: []SchemaDiscoveryDatasource{
				{Name: "local-kurtosis", Cluster: "local-kurtosis"},
			},
		},
		proxy.NewRouter(log, []proxy.ClientRoute{
			{Name: "hosted", Client: hosted},
			{Name: "local", Client: local, Local: true},
		}),
	).(*clickhouseSchemaClient)

	if err := client.initDatasources(); err != nil {
		t.Fatalf("initDatasources error = %v", err)
	}

	if err := client.refresh(context.Background()); err != nil {
		t.Fatalf("refresh error = %v", err)
	}

	if hosted.queryCount() != 0 {
		t.Fatalf("hosted proxy received %d schema queries, want 0", hosted.queryCount())
	}

	localQueries := local.snapshotQueries()
	if len(localQueries) == 0 {
		t.Fatalf("schema refresh made no queries through local proxy")
	}
	for _, query := range localQueries {
		if query.datasource != "local-kurtosis" {
			t.Fatalf("schema query datasource = %q, want local-kurtosis", query.datasource)
		}
		if got := query.params.Get("default_format"); got != "JSON" {
			t.Fatalf("schema query default_format = %q, want JSON", got)
		}
	}

	tables := client.GetAllTables()
	cluster := tables["local-kurtosis"]
	if cluster == nil {
		t.Fatalf("schema cache missing local-kurtosis cluster")
	}
	if _, ok := cluster.Tables["otel.otel_logs"]; !ok {
		t.Fatalf("schema cache missing otel.otel_logs; tables = %#v", cluster.Tables)
	}
}

type schemaQuery struct {
	datasource string
	sql        string
	params     url.Values
}

type schemaProxyClient struct {
	url        string
	token      string
	clickhouse []types.DatasourceInfo

	mu      sync.Mutex
	queries []schemaQuery
}

func (c *schemaProxyClient) Start(_ context.Context) error { return nil }
func (c *schemaProxyClient) Stop(_ context.Context) error  { return nil }
func (c *schemaProxyClient) URL() string                   { return c.url }
func (c *schemaProxyClient) RegisterToken() string         { return c.token }
func (c *schemaProxyClient) RevokeToken()                  {}
func (c *schemaProxyClient) ClickHouseDatasources() []string {
	return schemaDatasourceNames(c.ClickHouseDatasourceInfo())
}
func (c *schemaProxyClient) ClickHouseDatasourceInfo() []types.DatasourceInfo {
	return append([]types.DatasourceInfo(nil), c.clickhouse...)
}
func (c *schemaProxyClient) ClickHouseQuery(_ context.Context, datasource, sql string, params url.Values) ([]byte, error) {
	c.mu.Lock()
	c.queries = append(c.queries, schemaQuery{
		datasource: datasource,
		sql:        sql,
		params:     cloneValues(params),
	})
	c.mu.Unlock()

	response := `{"meta":[],"data":[],"rows":0}`
	switch {
	case strings.Contains(sql, "SELECT currentDatabase()"):
		response = `{"meta":[{"name":"db"}],"data":[{"db":"otel"}],"rows":1}`
	case strings.Contains(sql, "SHOW TABLES"):
		response = `{"meta":[{"name":"name"}],"data":[{"name":"otel_logs"}],"rows":1}`
	case strings.Contains(sql, "SHOW CREATE TABLE"):
		response = `{"meta":[{"name":"statement"}],"data":[{"statement":"CREATE TABLE ` + "`" + `otel_logs` + "`" + ` (` + "`" + `Timestamp` + "`" + ` DateTime) ENGINE = MergeTree ORDER BY tuple()"}],"rows":1}`
	}

	return []byte(response), nil
}
func (c *schemaProxyClient) PrometheusDatasources() []string { return nil }
func (c *schemaProxyClient) PrometheusDatasourceInfo() []types.DatasourceInfo {
	return nil
}
func (c *schemaProxyClient) LokiDatasources() []string                          { return nil }
func (c *schemaProxyClient) LokiDatasourceInfo() []types.DatasourceInfo         { return nil }
func (c *schemaProxyClient) BenchmarkoorDatasourceInfo() []types.DatasourceInfo { return nil }
func (c *schemaProxyClient) EthNodeAvailable() bool                             { return false }
func (c *schemaProxyClient) EthNodeDatasourceInfo() []types.DatasourceInfo {
	return nil
}
func (c *schemaProxyClient) EmbeddingAvailable() bool         { return false }
func (c *schemaProxyClient) EmbeddingModel() string           { return "" }
func (c *schemaProxyClient) Discover(_ context.Context) error { return nil }
func (c *schemaProxyClient) EnsureAuthenticated(_ context.Context) error {
	return nil
}

func (c *schemaProxyClient) queryCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.queries)
}

func (c *schemaProxyClient) snapshotQueries() []schemaQuery {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]schemaQuery(nil), c.queries...)
}

func cloneValues(values url.Values) url.Values {
	if len(values) == 0 {
		return nil
	}

	clone := make(url.Values, len(values))
	for key, value := range values {
		clone[key] = append([]string(nil), value...)
	}

	return clone
}

func schemaDatasourceNames(infos []types.DatasourceInfo) []string {
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		names = append(names, info.Name)
	}

	return names
}

var _ proxy.Client = (*schemaProxyClient)(nil)
