package clickhouse

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSchemaClientStartFailsWithoutDiscoveryDatasources(t *testing.T) {
	client := NewClickHouseSchemaClient(logrus.New(), ClickHouseSchemaConfig{}, &stubProxySchemaAccess{}).(*clickhouseSchemaClient)

	err := client.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no ClickHouse schema discovery datasources configured")
}

func TestSchemaClientGetTableReturnsMissForUnknownTable(t *testing.T) {
	client := &clickhouseSchemaClient{
		clusters: map[string]*ClusterTables{
			"xatu": {
				ClusterName: "xatu",
				Tables: map[string]*TableSchema{
					"blocks": {Name: "blocks"},
				},
			},
		},
	}

	table, cluster, ok := client.GetTable("missing")
	assert.False(t, ok)
	assert.Nil(t, table)
	assert.Empty(t, cluster)
}

func TestSchemaClientQueryJSONReturnsDecodeErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{not-json"))
	}))
	defer server.Close()

	client := &clickhouseSchemaClient{
		log:         logrus.New(),
		cfg:         ClickHouseSchemaConfig{QueryTimeout: time.Second},
		queryClient: newClickhouseSchemaQueryClient(&stubProxySchemaAccess{baseURL: server.URL}, server.Client(), time.Second),
		clusters:    make(map[string]*ClusterTables),
		datasources: map[string]string{"xatu": "xatu"},
	}

	_, err := client.queryClient.queryJSON(context.Background(), "xatu", "SHOW TABLES")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decoding response")
}

func TestSchemaClientFetchTableNetworksRejectsInvalidIdentifiers(t *testing.T) {
	client := &clickhouseSchemaClient{
		log: logrus.New(),
		cfg: ClickHouseSchemaConfig{QueryTimeout: time.Second},
	}

	_, err := client.queryClient.fetchTableNetworks(context.Background(), "xatu", "invalid-name")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validating table name")
}

func TestParseCreateTableReturnsPartialSchemaWhenColumnListIsUnterminated(t *testing.T) {
	schema, err := parseCreateTable("blocks", "CREATE TABLE blocks (`slot` UInt64 ENGINE = MergeTree")
	require.NoError(t, err)
	assert.Equal(t, "blocks", schema.Name)
	assert.Equal(t, "MergeTree", schema.Engine)
	assert.Empty(t, schema.Columns)
}
