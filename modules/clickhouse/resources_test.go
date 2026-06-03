package clickhouse

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTableURISegments(t *testing.T) {
	tests := []struct {
		name string
		uri  string
		want []string
	}{
		{name: "cluster", uri: "clickhouse://tables/xatu", want: []string{"xatu"}},
		{name: "cluster and database", uri: "clickhouse://tables/xatu/mainnet", want: []string{"xatu", "mainnet"}},
		{name: "fully qualified", uri: "clickhouse://tables/xatu/mainnet/fct_block_head", want: []string{"xatu", "mainnet", "fct_block_head"}},
		{name: "xatu-cbt cluster", uri: "clickhouse://tables/xatu-cbt/mainnet/int_block", want: []string{"xatu-cbt", "mainnet", "int_block"}},
		{name: "trailing slash invalid", uri: "clickhouse://tables/xatu/", want: nil},
		{name: "leading slash invalid", uri: "clickhouse://tables//mainnet", want: nil},
		{name: "empty after prefix invalid", uri: "clickhouse://tables/", want: nil},
		{name: "missing prefix", uri: "clickhouse://something/db/table", want: nil},
		{name: "empty", uri: "", want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tableURISegments(tt.uri))
		})
	}
}

func TestTableKey(t *testing.T) {
	assert.Equal(t, "logs_internal.logs", tableKey("logs_internal", "logs"))
	assert.Equal(t, "default.events", tableKey("default", "events"))
}

// stubSchemaClient is a ClickHouseSchemaClient backed by fixed in-memory data.
type stubSchemaClient struct {
	clusters map[string]*ClusterTables
}

func (s *stubSchemaClient) Start(_ context.Context) error                   { return nil }
func (s *stubSchemaClient) Stop() error                                     { return nil }
func (s *stubSchemaClient) WaitForReady(_ context.Context) error            { return nil }
func (s *stubSchemaClient) UpdateDatasources(_ []SchemaDiscoveryDatasource) {}
func (s *stubSchemaClient) GetAllTables() map[string]*ClusterTables         { return s.clusters }

func (s *stubSchemaClient) GetClusterTables(clusterName string) (*ClusterTables, bool) {
	c, ok := s.clusters[clusterName]

	return c, ok
}

func (s *stubSchemaClient) GetTableInCluster(clusterName, database, tableName string) (*TableSchema, bool) {
	c, ok := s.clusters[clusterName]
	if !ok {
		return nil, false
	}

	schema, ok := c.Tables[tableKey(database, tableName)]

	return schema, ok
}

func newStubSchemaClient() *stubSchemaClient {
	col := []TableColumn{{Name: "slot", Type: "UInt32"}}

	return &stubSchemaClient{
		clusters: map[string]*ClusterTables{
			"xatu": {
				ClusterName: "xatu",
				LastUpdated: time.Unix(0, 0).UTC(),
				Tables: map[string]*TableSchema{
					tableKey("default", "beacon_blocks"): {Database: "default", Name: "beacon_blocks", Columns: col},
				},
			},
			"xatu-cbt": {
				ClusterName: "xatu-cbt",
				LastUpdated: time.Unix(0, 0).UTC(),
				Tables: map[string]*TableSchema{
					tableKey("mainnet", "fct_block"):       {Database: "mainnet", Name: "fct_block", Columns: col},
					tableKey("mainnet", "fct_attestation"): {Database: "mainnet", Name: "fct_attestation", Columns: col},
					tableKey("holesky", "fct_block"):       {Database: "holesky", Name: "fct_block", Columns: col},
				},
			},
		},
	}
}

func TestTablesListHandler(t *testing.T) {
	client := newStubSchemaClient()

	out, err := createTablesListHandler(client)(context.Background(), "clickhouse://tables")
	require.NoError(t, err)

	var resp TablesListResponse
	require.NoError(t, json.Unmarshal([]byte(out), &resp))

	assert.Len(t, resp.Clusters, 2)
	assert.Equal(t, 1, resp.Clusters["xatu"].TableCount)
	assert.Equal(t, 3, resp.Clusters["xatu-cbt"].TableCount)
}

func TestClusterTablesHandler(t *testing.T) {
	client := newStubSchemaClient()
	handler := createClusterTablesHandler(client)

	out, err := handler(context.Background(), "clickhouse://tables/xatu-cbt")
	require.NoError(t, err)

	var resp TablesListResponse
	require.NoError(t, json.Unmarshal([]byte(out), &resp))

	assert.Len(t, resp.Clusters, 1)
	assert.Equal(t, 3, resp.Clusters["xatu-cbt"].TableCount)

	_, err = handler(context.Background(), "clickhouse://tables/nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "available clusters are xatu, xatu-cbt")
}

func TestDatabaseTablesHandler(t *testing.T) {
	client := newStubSchemaClient()
	handler := createDatabaseTablesHandler(client)

	out, err := handler(context.Background(), "clickhouse://tables/xatu-cbt/mainnet")
	require.NoError(t, err)

	var resp TablesListResponse
	require.NoError(t, json.Unmarshal([]byte(out), &resp))

	require.Len(t, resp.Clusters, 1)
	assert.Equal(t, 2, resp.Clusters["xatu-cbt"].TableCount)
	for _, table := range resp.Clusters["xatu-cbt"].Tables {
		assert.Equal(t, "mainnet", table.Database)
	}
}

func TestTableDetailHandler(t *testing.T) {
	client := newStubSchemaClient()
	handler := createTableDetailHandler(logrus.New(), client)

	out, err := handler(context.Background(), "clickhouse://tables/xatu-cbt/mainnet/fct_block")
	require.NoError(t, err)

	var resp TableDetailResponse
	require.NoError(t, json.Unmarshal([]byte(out), &resp))

	assert.Equal(t, "xatu-cbt", resp.Cluster)
	require.NotNil(t, resp.Table)
	assert.Equal(t, "mainnet", resp.Table.Database)
	assert.Equal(t, "fct_block", resp.Table.Name)

	// Same table name exists in the xatu-cbt holesky database and not in xatu;
	// the cluster+database scoping must not leak across them.
	_, err = handler(context.Background(), "clickhouse://tables/xatu/mainnet/fct_block")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found in cluster \"xatu\"")

	_, err = handler(context.Background(), "clickhouse://tables/nope/mainnet/fct_block")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}
