package clickhouse

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResourceHelpersHandleInvalidURIsAndDeduplicateTables(t *testing.T) {
	client := &stubSchemaClient{
		clusters: map[string]*ClusterTables{
			"xatu": {
				ClusterName: "xatu",
				Tables: map[string]*TableSchema{
					"blocks": {Name: "blocks"},
					"slots":  {Name: "slots"},
				},
			},
			"xatu-cbt": {
				ClusterName: "xatu-cbt",
				Tables: map[string]*TableSchema{
					"blocks": {Name: "blocks"},
				},
			},
		},
	}

	_, err := createTableDetailHandler(logrus.New(), client)(context.Background(), "clickhouse://invalid/blocks")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid table URI")
	assert.Equal(t, []string{"blocks", "slots"}, listAvailableTables(client))
}

func TestSchemaClientWaitForReadyHonorsContext(t *testing.T) {
	client := &clickhouseSchemaClient{ready: make(chan struct{})}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := client.WaitForReady(ctx)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestSchemaClientStartWaitAndStopRefreshesSchema(t *testing.T) {
	var showTablesCalls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "xatu", r.Header.Get("X-Datasource"))

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		switch string(body) {
		case "SHOW TABLES":
			showTablesCalls.Add(1)
			require.NoError(t, json.NewEncoder(w).Encode(clickhouseJSONResponse{
				Meta: []clickhouseJSONMeta{{Name: "name"}},
				Data: []map[string]any{{"name": "blocks"}},
			}))
		case "SHOW CREATE TABLE `blocks`":
			require.NoError(t, json.NewEncoder(w).Encode(clickhouseJSONResponse{
				Meta: []clickhouseJSONMeta{{Name: "statement"}},
				Data: []map[string]any{{
					"statement": "CREATE TABLE blocks (`meta_network_name` LowCardinality(String), `slot` UInt64) ENGINE = MergeTree",
				}},
			}))
		case "SELECT DISTINCT meta_network_name FROM `blocks` WHERE meta_network_name IS NOT NULL AND meta_network_name != '' LIMIT 1000":
			require.NoError(t, json.NewEncoder(w).Encode(clickhouseJSONResponse{
				Meta: []clickhouseJSONMeta{{Name: "meta_network_name"}},
				Data: []map[string]any{{"meta_network_name": "mainnet"}},
			}))
		default:
			t.Fatalf("unexpected SQL: %q", string(body))
		}
	}))
	defer server.Close()

	proxyAccess := &stubProxySchemaAccess{baseURL: server.URL}
	client := NewClickHouseSchemaClient(logrus.New(), ClickHouseSchemaConfig{
		Datasources:     []SchemaDiscoveryDatasource{{Name: "xatu", Cluster: "xatu"}},
		RefreshInterval: 10 * time.Millisecond,
		QueryTimeout:    50 * time.Millisecond,
	}, proxyAccess).(*clickhouseSchemaClient)
	client.httpClient = server.Client()

	require.NoError(t, client.Start(context.Background()))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	require.NoError(t, client.WaitForReady(ctx))

	table, cluster, ok := client.GetTable("blocks")
	require.True(t, ok)
	assert.Equal(t, "xatu", cluster)
	assert.Equal(t, "blocks", table.Name)
	assert.Equal(t, []string{"mainnet"}, table.Networks)

	assert.Eventually(t, func() bool {
		return showTablesCalls.Load() >= 2
	}, time.Second, 10*time.Millisecond)

	require.NoError(t, client.Stop())
	assert.GreaterOrEqual(t, proxyAccess.authCalls, 3)
}

func TestSchemaClientRefreshAndQueryErrorPaths(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		switch r.Header.Get("X-Datasource") {
		case "xatu":
			switch string(body) {
			case "SHOW TABLES":
				require.NoError(t, json.NewEncoder(w).Encode(clickhouseJSONResponse{
					Meta: []clickhouseJSONMeta{{Name: "name"}},
					Data: []map[string]any{
						{"name": "blocks"},
						{"name": "bad_table"},
					},
				}))
			case "SHOW CREATE TABLE `blocks`":
				require.NoError(t, json.NewEncoder(w).Encode(clickhouseJSONResponse{
					Meta: []clickhouseJSONMeta{{Name: "statement"}},
					Data: []map[string]any{{
						"statement": "CREATE TABLE blocks (`meta_network_name` String, `slot` UInt64) ENGINE = MergeTree",
					}},
				}))
			case "SHOW CREATE TABLE `bad_table`":
				http.Error(w, "schema lookup failed", http.StatusBadGateway)
			case "SELECT DISTINCT meta_network_name FROM `blocks` WHERE meta_network_name IS NOT NULL AND meta_network_name != '' LIMIT 1000":
				require.NoError(t, json.NewEncoder(w).Encode(clickhouseJSONResponse{
					Meta: []clickhouseJSONMeta{{Name: "meta_network_name"}},
					Data: []map[string]any{{"meta_network_name": "mainnet"}},
				}))
			case "SHOW CREATE TABLE `empty_table`":
				require.NoError(t, json.NewEncoder(w).Encode(clickhouseJSONResponse{
					Meta: []clickhouseJSONMeta{{Name: "statement"}},
				}))
			case "SHOW CREATE TABLE `missing_cols`":
				require.NoError(t, json.NewEncoder(w).Encode(clickhouseJSONResponse{
					Data: []map[string]any{{"statement": "CREATE TABLE missing_cols (`slot` UInt64) ENGINE = MergeTree"}},
				}))
			case "SELECT DISTINCT meta_network_name FROM `missing_networks` WHERE meta_network_name IS NOT NULL AND meta_network_name != '' LIMIT 1000":
				require.NoError(t, json.NewEncoder(w).Encode(clickhouseJSONResponse{
					Data: []map[string]any{{"meta_network_name": "mainnet"}},
				}))
			case "SHOW TABLES MISSING":
				require.NoError(t, json.NewEncoder(w).Encode(clickhouseJSONResponse{
					Data: []map[string]any{{"name": "blocks"}},
				}))
			case "CLICKHOUSE ERROR":
				require.NoError(t, json.NewEncoder(w).Encode(clickhouseJSONResponse{
					Err: &clickhouseJSONError{Code: 321, Message: "broken query"},
				}))
			case "BAD STATUS":
				http.Error(w, "gateway down", http.StatusBadGateway)
			default:
				t.Fatalf("unexpected SQL for xatu: %q", string(body))
			}
		case "missing-list":
			require.Equal(t, "SHOW TABLES", string(body))
			require.NoError(t, json.NewEncoder(w).Encode(clickhouseJSONResponse{
				Data: []map[string]any{{"name": "blocks"}},
			}))
		case "broken":
			require.Equal(t, "SHOW TABLES", string(body))
			http.Error(w, "cluster unavailable", http.StatusServiceUnavailable)
		default:
			t.Fatalf("unexpected datasource: %q", r.Header.Get("X-Datasource"))
		}
	}))
	defer server.Close()

	client := &clickhouseSchemaClient{
		log:         logrus.New(),
		cfg:         ClickHouseSchemaConfig{QueryTimeout: time.Second},
		queryClient: newClickhouseSchemaQueryClient(&stubProxySchemaAccess{baseURL: server.URL}, server.Client(), time.Second),
		clusters:    make(map[string]*ClusterTables),
		datasources: map[string]string{"xatu": "xatu", "broken": "broken"},
	}

	t.Run("refresh keeps successful clusters and skips failed tables", func(t *testing.T) {
		require.NoError(t, client.refresh(context.Background()))

		require.Contains(t, client.clusters, "xatu")
		assert.NotContains(t, client.clusters, "broken")
		assert.Contains(t, client.clusters["xatu"].Tables, "blocks")
		assert.NotContains(t, client.clusters["xatu"].Tables, "bad_table")
		assert.Equal(t, []string{"mainnet"}, client.clusters["xatu"].Tables["blocks"].Networks)
	})

	t.Run("queryJSON reports upstream status and clickhouse payload errors", func(t *testing.T) {
		_, err := client.queryClient.queryJSON(context.Background(), "xatu", "BAD STATUS")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "query failed (502)")

		_, err = client.queryClient.queryJSON(context.Background(), "xatu", "CLICKHOUSE ERROR")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "query error (321): broken query")
	})

	t.Run("fetch helpers surface missing columns and invalid identifiers", func(t *testing.T) {
		_, err := client.queryClient.fetchTableList(context.Background(), "missing-list")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "SHOW TABLES response missing columns")

		_, err = client.queryClient.fetchTableSchema(context.Background(), "xatu", "invalid-name")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "validating table name")

		_, err = client.queryClient.fetchTableSchema(context.Background(), "xatu", "empty_table")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty CREATE TABLE statement")

		_, err = client.queryClient.fetchTableSchema(context.Background(), "xatu", "missing_cols")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "SHOW CREATE TABLE response missing columns")

		_, err = client.queryClient.fetchTableNetworks(context.Background(), "xatu", "missing_networks")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "network query response missing columns")
	})
}
