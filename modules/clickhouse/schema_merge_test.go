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

// multiDBProxyClient fakes a cluster shaped like the hosted clickhouse-raw: a
// populated default database plus per-dataset databases (external/internal).
type multiDBProxyClient struct {
	schemaProxyClient

	mu      sync.Mutex
	queries []string
}

func (c *multiDBProxyClient) ClickHouseQuery(_ context.Context, _, sql string, _ url.Values) ([]byte, error) {
	c.mu.Lock()
	c.queries = append(c.queries, sql)
	c.mu.Unlock()

	response := `{"meta":[],"data":[],"rows":0}`

	switch {
	case strings.Contains(sql, "SELECT currentDatabase()"):
		response = `{"meta":[{"name":"db"}],"data":[{"db":"default"}],"rows":1}`
	case strings.Contains(sql, "SHOW DATABASES"):
		response = `{"meta":[{"name":"name"}],"data":[{"name":"default"},{"name":"external"},{"name":"system"}],"rows":3}`
	case strings.Contains(sql, "SHOW TABLES FROM `external`"):
		response = `{"meta":[{"name":"name"}],"data":[{"name":"otel_logs"}],"rows":1}`
	case strings.Contains(sql, "SHOW TABLES"):
		response = `{"meta":[{"name":"name"}],"data":[{"name":"beacon_api_eth_v1_events_block"}],"rows":1}`
	case strings.Contains(sql, "SHOW CREATE TABLE"):
		response = `{"meta":[{"name":"statement"}],"data":[{"statement":"CREATE TABLE t (` + "`" + `Timestamp` + "`" + ` DateTime) ENGINE = MergeTree ORDER BY tuple()"}],"rows":1}`
	}

	return []byte(response), nil
}

// TestSchemaRefreshMergesDefaultAndPerDatabaseTables guards against the
// default-database short-circuit: a non-empty default database must not hide
// tables living in other databases of the same cluster, or live-schema
// validation wrongly demotes examples that reference them.
func TestSchemaRefreshMergesDefaultAndPerDatabaseTables(t *testing.T) {
	t.Parallel()

	upstream := &multiDBProxyClient{
		schemaProxyClient: schemaProxyClient{
			url:        "https://hosted.proxy",
			token:      "token",
			clickhouse: []types.DatasourceInfo{{Name: "clickhouse-raw"}},
		},
	}

	log := logrus.New()
	log.SetOutput(io.Discard)

	client := NewSchemaClient(
		log,
		SchemaConfig{
			QueryTimeout: time.Second,
			Datasources: []SchemaDiscoveryDatasource{
				{Name: "clickhouse-raw", Cluster: "clickhouse-raw"},
			},
		},
		proxy.NewRouter(log, []proxy.ClientRoute{
			{Name: "hosted", Client: upstream},
		}),
	).(*clickhouseSchemaClient)

	if err := client.initDatasources(); err != nil {
		t.Fatalf("initDatasources error = %v", err)
	}

	if err := client.refresh(context.Background()); err != nil {
		t.Fatalf("refresh error = %v", err)
	}

	cluster, ok := client.GetClusterTables("clickhouse-raw")
	if !ok {
		t.Fatal("schema cache missing clickhouse-raw cluster")
	}

	if _, ok := cluster.Tables["default.beacon_api_eth_v1_events_block"]; !ok {
		t.Fatalf("missing default-database table; tables = %#v", cluster.Tables)
	}

	if _, ok := cluster.Tables["external.otel_logs"]; !ok {
		t.Fatalf("missing per-database table external.otel_logs; tables = %#v", cluster.Tables)
	}
}
