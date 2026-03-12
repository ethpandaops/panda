package clickhouse

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClickHouseSchemaClientAppliesDefaultTimeouts(t *testing.T) {
	client := NewClickHouseSchemaClient(logrus.New(), ClickHouseSchemaConfig{
		Datasources: []SchemaDiscoveryDatasource{{Name: "xatu", Cluster: "xatu"}},
	}, &stubProxySchemaAccess{}).(*clickhouseSchemaClient)

	assert.Equal(t, DefaultSchemaRefreshInterval, client.cfg.RefreshInterval)
	assert.Equal(t, DefaultSchemaQueryTimeout, client.cfg.QueryTimeout)
}

func TestSchemaClientInitDatasourcesRequiresProxyAndSkipsIncompleteEntries(t *testing.T) {
	client := NewClickHouseSchemaClient(logrus.New(), ClickHouseSchemaConfig{
		Datasources: []SchemaDiscoveryDatasource{
			{Name: "", Cluster: "ignored"},
			{Name: "xatu", Cluster: ""},
			{Name: "xatu", Cluster: "main"},
		},
	}, nil).(*clickhouseSchemaClient)

	err := client.initDatasources()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "proxy service is required")

	client.queryClient.proxySvc = &stubProxySchemaAccess{}
	require.NoError(t, client.initDatasources())
	assert.Equal(t, map[string]string{"main": "xatu"}, client.datasources)
}

func TestSchemaClientWaitForReadyReturnsOnceReady(t *testing.T) {
	client := &clickhouseSchemaClient{ready: make(chan struct{})}
	close(client.ready)

	require.NoError(t, client.WaitForReady(context.Background()))
}

func TestSchemaClientStartClosesReadyWhenInitialRefreshFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
	}))
	defer server.Close()

	client := NewClickHouseSchemaClient(logrus.New(), ClickHouseSchemaConfig{
		Datasources:     []SchemaDiscoveryDatasource{{Name: "xatu", Cluster: "xatu"}},
		RefreshInterval: time.Hour,
		QueryTimeout:    20 * time.Millisecond,
	}, &stubProxySchemaAccess{baseURL: server.URL}).(*clickhouseSchemaClient)
	client.queryClient.httpClient = server.Client()

	require.NoError(t, client.Start(context.Background()))
	t.Cleanup(func() {
		require.NoError(t, client.Stop())
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	require.NoError(t, client.WaitForReady(ctx))
	assert.Empty(t, client.GetAllTables())
}

func TestSchemaClientRefreshWithoutDatasourcesLeavesExistingState(t *testing.T) {
	client := &clickhouseSchemaClient{
		log: logrus.New(),
		clusters: map[string]*ClusterTables{
			"xatu": {
				ClusterName: "xatu",
				Tables:      map[string]*TableSchema{"blocks": {Name: "blocks"}},
			},
		},
		datasources: map[string]string{},
	}

	require.NoError(t, client.refresh(context.Background()))
	require.Contains(t, client.clusters, "xatu")
	assert.Contains(t, client.clusters["xatu"].Tables, "blocks")
}

func TestFetchTableNetworksSortsAndSkipsEmptyValues(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.Equal(t,
			"SELECT DISTINCT meta_network_name FROM `blocks` WHERE meta_network_name IS NOT NULL AND meta_network_name != '' LIMIT 1000",
			string(body),
		)
		require.NoError(t, json.NewEncoder(w).Encode(clickhouseJSONResponse{
			Meta: []clickhouseJSONMeta{{Name: "meta_network_name"}},
			Data: []map[string]any{
				{"meta_network_name": "hoodi"},
				{"meta_network_name": ""},
				{"meta_network_name": "mainnet"},
			},
		}))
	}))
	defer server.Close()

	client := &clickhouseSchemaClient{
		log:         logrus.New(),
		cfg:         ClickHouseSchemaConfig{QueryTimeout: time.Second},
		queryClient: newClickhouseSchemaQueryClient(&stubProxySchemaAccess{baseURL: server.URL}, server.Client(), time.Second),
		clusters:    make(map[string]*ClusterTables),
		datasources: map[string]string{"xatu": "xatu"},
	}

	networks, err := client.queryClient.fetchTableNetworks(context.Background(), "xatu", "blocks")
	require.NoError(t, err)
	assert.Equal(t, []string{"hoodi", "mainnet"}, networks)
}

func TestParseCreateTableHandlesStatementsWithoutColumnBlock(t *testing.T) {
	schema, err := parseCreateTable("blocks", "CREATE TABLE blocks ENGINE = TinyLog")
	require.NoError(t, err)
	assert.Equal(t, "blocks", schema.Name)
	assert.Equal(t, "TinyLog", schema.Engine)
	assert.Empty(t, schema.Columns)
}

func TestCleanColumnTypeHandlesMaterializedAndAliasClauses(t *testing.T) {
	assert.Equal(t, "UInt64", cleanColumnType("UInt64 MATERIALIZED slot + 1"))
	assert.Equal(t, "String", cleanColumnType("String ALIAS lower(name)"))
}

func TestSchemaClientQueryJSONPropagatesAuthorizeErrors(t *testing.T) {
	client := &clickhouseSchemaClient{
		log: logrus.New(),
		cfg: ClickHouseSchemaConfig{QueryTimeout: time.Second},
		queryClient: newClickhouseSchemaQueryClient(
			failingProxySchemaAccess{
				baseURL: "http://proxy.example",
				err:     errors.New("missing token"),
			},
			&http.Client{},
			time.Second,
		),
	}

	_, err := client.queryClient.queryJSON(context.Background(), "xatu", "SHOW TABLES")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authorizing request")
}

type failingProxySchemaAccess struct {
	baseURL string
	err     error
}

func (s failingProxySchemaAccess) URL() string { return s.baseURL }

func (s failingProxySchemaAccess) AuthorizeRequest(*http.Request) error { return s.err }

func (s failingProxySchemaAccess) ClickHouseDatasources() []string { return nil }
