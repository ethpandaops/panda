package clickhouse

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/proxy"
	"github.com/ethpandaops/panda/pkg/types"
)

func TestSchemaRefreshRoutesDatasourceToOwningProxy(t *testing.T) {
	t.Parallel()

	transport := &schemaRecordingTransport{}

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

	client := NewClickHouseSchemaClient(
		log,
		ClickHouseSchemaConfig{
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
	client.httpClient = &http.Client{Transport: transport}

	if err := client.initDatasources(); err != nil {
		t.Fatalf("initDatasources error = %v", err)
	}

	if err := client.refresh(context.Background()); err != nil {
		t.Fatalf("refresh error = %v", err)
	}

	if len(transport.hosts) == 0 {
		t.Fatalf("schema refresh made no HTTP requests")
	}
	for _, host := range transport.hosts {
		if host != "local.proxy" {
			t.Fatalf("schema request host = %q, want local.proxy; all hosts: %v", host, transport.hosts)
		}
	}
	for _, auth := range transport.authHeaders {
		if auth != "Bearer local-token" {
			t.Fatalf("schema request Authorization = %q, want local token", auth)
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

type schemaRecordingTransport struct {
	hosts       []string
	authHeaders []string
}

func (t *schemaRecordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}

	t.hosts = append(t.hosts, req.URL.Host)
	t.authHeaders = append(t.authHeaders, req.Header.Get("Authorization"))

	sql := string(body)
	response := `{"meta":[],"data":[],"rows":0}`
	switch {
	case strings.Contains(sql, "SELECT currentDatabase()"):
		response = `{"meta":[{"name":"db"}],"data":[{"db":"otel"}],"rows":1}`
	case strings.Contains(sql, "SHOW TABLES"):
		response = `{"meta":[{"name":"name"}],"data":[{"name":"otel_logs"}],"rows":1}`
	case strings.Contains(sql, "SHOW CREATE TABLE"):
		response = `{"meta":[{"name":"statement"}],"data":[{"statement":"CREATE TABLE ` + "`" + `otel_logs` + "`" + ` (` + "`" + `Timestamp` + "`" + ` DateTime) ENGINE = MergeTree ORDER BY tuple()"}],"rows":1}`
	}

	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(response)),
	}, nil
}

type schemaProxyClient struct {
	url        string
	token      string
	clickhouse []types.DatasourceInfo
}

func (c *schemaProxyClient) Start(_ context.Context) error { return nil }
func (c *schemaProxyClient) Stop(_ context.Context) error  { return nil }
func (c *schemaProxyClient) URL() string                   { return c.url }
func (c *schemaProxyClient) RegisterToken(_ string) string { return c.token }
func (c *schemaProxyClient) RevokeToken(_ string)          {}
func (c *schemaProxyClient) ClickHouseDatasources() []string {
	return schemaDatasourceNames(c.ClickHouseDatasourceInfo())
}
func (c *schemaProxyClient) ClickHouseDatasourceInfo() []types.DatasourceInfo {
	return append([]types.DatasourceInfo(nil), c.clickhouse...)
}
func (c *schemaProxyClient) PrometheusDatasources() []string { return nil }
func (c *schemaProxyClient) PrometheusDatasourceInfo() []types.DatasourceInfo {
	return nil
}
func (c *schemaProxyClient) LokiDatasources() []string                  { return nil }
func (c *schemaProxyClient) LokiDatasourceInfo() []types.DatasourceInfo { return nil }
func (c *schemaProxyClient) EthNodeAvailable() bool                     { return false }
func (c *schemaProxyClient) EmbeddingAvailable() bool                   { return false }
func (c *schemaProxyClient) EmbeddingModel() string                     { return "" }
func (c *schemaProxyClient) Discover(_ context.Context) error           { return nil }
func (c *schemaProxyClient) EnsureAuthenticated(_ context.Context) error {
	return nil
}

func schemaDatasourceNames(infos []types.DatasourceInfo) []string {
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		names = append(names, info.Name)
	}

	return names
}

var _ proxy.Client = (*schemaProxyClient)(nil)
