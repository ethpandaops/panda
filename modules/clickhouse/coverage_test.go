package clickhouse

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	modulepkg "github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/proxy"
	"github.com/ethpandaops/panda/pkg/types"
)

func TestClickHouseConfigExamplesModuleAndResources(t *testing.T) {
	var cfg SchemaDiscoveryConfig
	assert.True(t, cfg.IsEnabled())
	disabled := false
	cfg.Enabled = &disabled
	assert.False(t, cfg.IsEnabled())

	firstExamples, err := loadExamples()
	require.NoError(t, err)
	secondExamples, err := loadExamples()
	require.NoError(t, err)
	require.NotEmpty(t, firstExamples)
	require.NotEmpty(t, secondExamples)

	module := New()
	require.Equal(t, "clickhouse", module.Name())
	require.NoError(t, module.Init([]byte("schema_discovery:\n  datasources:\n    - name: xatu\n      cluster: main\n    - name: \"\"\n      cluster: ignored\n")))
	module.ApplyDefaults()
	require.NoError(t, module.Validate())
	assert.NotEmpty(t, module.Examples())
	assert.Contains(t, module.PythonAPIDocs(), "clickhouse")
	assert.Contains(t, module.GettingStartedSnippet(), "ClickHouse Cluster Rules")

	registry := &stubResourceRegistry{}
	module.schemaClient = &stubClickHouseSchemaClient{clusters: map[string]*ClusterTables{
		"xatu": {
			ClusterName: "xatu",
			LastUpdated: time.Unix(100, 0).UTC(),
			Tables: map[string]*TableSchema{
				"blocks": {Name: "blocks", Columns: []TableColumn{{Name: "slot", Type: "UInt64"}}, HasNetworkCol: true},
				"slots":  {Name: "slots", Columns: []TableColumn{{Name: "root", Type: "String"}}},
			},
		},
	}}
	require.NoError(t, module.RegisterResources(logrus.New(), registry))
	require.Len(t, registry.staticResources, 1)
	require.Len(t, registry.templateResources, 1)

	listPayload, err := createTablesListHandler(module.schemaClient)(context.Background(), "clickhouse://tables")
	require.NoError(t, err)

	var listResponse TablesListResponse
	require.NoError(t, json.Unmarshal([]byte(listPayload), &listResponse))
	require.Contains(t, listResponse.Clusters, "xatu")
	assert.Equal(t, 2, listResponse.Clusters["xatu"].TableCount)

	detailPayload, err := createTableDetailHandler(logrus.New(), module.schemaClient)(context.Background(), "clickhouse://tables/BLOCKS")
	require.NoError(t, err)
	var detailResponse TableDetailResponse
	require.NoError(t, json.Unmarshal([]byte(detailPayload), &detailResponse))
	require.NotNil(t, detailResponse.Table)
	assert.Equal(t, "blocks", detailResponse.Table.Name)
	assert.Equal(t, "xatu", detailResponse.Cluster)

	available := listAvailableTables(module.schemaClient)
	sort.Strings(available)
	assert.Equal(t, []string{"blocks", "slots"}, available)
	assert.Equal(t, "table", extractTableName("clickhouse://tables/table"))
	assert.Empty(t, extractTableName("bad://uri"))
}

func TestClickHouseSchemaClientRefreshesFromProxy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		switch string(body) {
		case "SHOW TABLES":
			require.NoError(t, json.NewEncoder(w).Encode(clickhouseJSONResponse{
				Meta: []clickhouseJSONMeta{{Name: "name"}},
				Data: []map[string]any{{"name": "blocks"}, {"name": "blocks_local"}},
			}))
		case "SHOW CREATE TABLE `blocks`":
			require.NoError(t, json.NewEncoder(w).Encode(clickhouseJSONResponse{
				Meta: []clickhouseJSONMeta{{Name: "statement"}},
				Data: []map[string]any{{
					"statement": "CREATE TABLE blocks (`meta_network_name` String, `slot` UInt64) ENGINE = MergeTree COMMENT 'Beacon blocks'",
				}},
			}))
		case "SELECT DISTINCT meta_network_name FROM `blocks` WHERE meta_network_name IS NOT NULL AND meta_network_name != '' LIMIT 1000":
			require.NoError(t, json.NewEncoder(w).Encode(clickhouseJSONResponse{
				Meta: []clickhouseJSONMeta{{Name: "meta_network_name"}},
				Data: []map[string]any{{"meta_network_name": "hoodi"}, {"meta_network_name": "mainnet"}},
			}))
		default:
			t.Fatalf("unexpected SQL: %q", string(body))
		}
	}))
	defer server.Close()

	proxyAccess := &stubProxySchemaAccess{baseURL: server.URL, datasources: []string{"xatu"}}
	client := NewClickHouseSchemaClient(logrus.New(), ClickHouseSchemaConfig{
		Datasources:     []SchemaDiscoveryDatasource{{Name: "xatu", Cluster: "xatu"}},
		RefreshInterval: 10 * time.Millisecond,
		QueryTimeout:    50 * time.Millisecond,
	}, proxyAccess).(*clickhouseSchemaClient)
	client.queryClient.httpClient = server.Client()

	require.NoError(t, client.Start(context.Background()))
	t.Cleanup(func() {
		require.NoError(t, client.Stop())
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, client.WaitForReady(ctx))

	table, cluster, found := client.GetTable("blocks")
	require.True(t, found)
	assert.Equal(t, "xatu", cluster)
	assert.Equal(t, "MergeTree", table.Engine)
	assert.Equal(t, []string{"hoodi", "mainnet"}, table.Networks)

	tables, err := client.queryClient.fetchTableList(context.Background(), "xatu")
	require.NoError(t, err)
	assert.Equal(t, []string{"blocks"}, tables)

	schema, err := client.queryClient.fetchTableSchema(context.Background(), "xatu", "blocks")
	require.NoError(t, err)
	assert.True(t, schema.HasNetworkCol)
	assert.Equal(t, "Beacon blocks", schema.Comment)
}

type stubClickHouseSchemaClient struct {
	clusters map[string]*ClusterTables
}

type stubSchemaClient = stubClickHouseSchemaClient

func (s *stubClickHouseSchemaClient) Start(context.Context) error             { return nil }
func (s *stubClickHouseSchemaClient) Stop() error                             { return nil }
func (s *stubClickHouseSchemaClient) WaitForReady(context.Context) error      { return nil }
func (s *stubClickHouseSchemaClient) GetAllTables() map[string]*ClusterTables { return s.clusters }
func (s *stubClickHouseSchemaClient) GetTable(tableName string) (*TableSchema, string, bool) {
	for clusterName, cluster := range s.clusters {
		if table, ok := cluster.Tables[tableName]; ok {
			return table, clusterName, true
		}
	}

	return nil, "", false
}

type stubResourceRegistry struct {
	staticResources   []types.StaticResource
	templateResources []types.TemplateResource
}

func (s *stubResourceRegistry) RegisterStatic(res types.StaticResource) {
	s.staticResources = append(s.staticResources, res)
}

func (s *stubResourceRegistry) RegisterTemplate(res types.TemplateResource) {
	s.templateResources = append(s.templateResources, res)
}

type stubProxySchemaAccess struct {
	datasources []string
	baseURL     string
	authCalls   int
}

func (s *stubProxySchemaAccess) URL() string { return s.baseURL }

func (s *stubProxySchemaAccess) AuthorizeRequest(req *http.Request) error {
	s.authCalls++
	req.Header.Set("Authorization", "Bearer test-token")
	return nil
}

func (s *stubProxySchemaAccess) ClickHouseDatasources() []string { return s.datasources }

var _ modulepkg.ResourceRegistry = (*stubResourceRegistry)(nil)
var _ ClickHouseSchemaClient = (*stubClickHouseSchemaClient)(nil)
var _ proxy.ClickHouseSchemaAccess = (*stubProxySchemaAccess)(nil)

func _unusedMCPResource(_ mcp.Resource) {}
