package clickhouse

import (
	"context"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/proxy"
	"github.com/ethpandaops/panda/pkg/types"
)

type localBackingSchemaProxyClient struct {
	schemaProxyClient
}

func (c *localBackingSchemaProxyClient) ClickHouseQuery(
	_ context.Context,
	_ string,
	sql string,
	_ url.Values,
) ([]byte, error) {
	response := `{"meta":[],"data":[],"rows":0}`

	switch {
	case strings.Contains(sql, "SELECT currentDatabase()"):
		response = `{"meta":[{"name":"db"}],"data":[{"db":"sample_db"}],"rows":1}`
	case strings.Contains(sql, "SHOW DATABASES"):
		response = `{"meta":[{"name":"name"}],"data":[{"name":"system"}],"rows":1}`
	case strings.Contains(sql, "SHOW TABLES"):
		response = `{"meta":[{"name":"name"}],"data":[{"name":"synthetic_dist"}],"rows":1}`
	case strings.Contains(sql, "`synthetic_dist_local`"):
		response = `{"meta":[{"name":"statement"}],"data":[{"statement":"CREATE TABLE ` + "`" + `synthetic_dist_local` + "`" + ` (` + "`" + `sample_id` + "`" + ` UInt64, ` + "`" + `sample_value` + "`" + ` String) ENGINE = ReplicatedMergeTree PARTITION BY intDiv(sample_id, 32) ORDER BY (sample_id, sample_value) PRIMARY KEY sample_id"}],"rows":1}`
	case strings.Contains(sql, "`synthetic_dist`"):
		response = `{"meta":[{"name":"statement"}],"data":[{"statement":"CREATE TABLE ` + "`" + `synthetic_dist` + "`" + ` (` + "`" + `sample_id` + "`" + ` UInt64, ` + "`" + `sample_value` + "`" + ` String) ENGINE = Distributed(cluster, sample_db, synthetic_dist_local, rand())"}],"rows":1}`
	}

	return []byte(response), nil
}

func TestSchemaRefreshCopiesKeyClausesFromLocalBackingTable(t *testing.T) {
	t.Parallel()

	upstream := &localBackingSchemaProxyClient{
		schemaProxyClient: schemaProxyClient{
			url:        "https://hosted.proxy",
			token:      "token",
			clickhouse: []types.DatasourceInfo{{Name: "synthetic-cluster"}},
		},
	}

	log := logrus.New()
	log.SetOutput(io.Discard)

	client := NewSchemaClient(
		log,
		SchemaConfig{
			QueryTimeout: time.Second,
			Datasources: []SchemaDiscoveryDatasource{
				{Name: "synthetic-cluster", Cluster: "synthetic-cluster"},
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

	cluster, ok := client.GetClusterTables("synthetic-cluster")
	if !ok {
		t.Fatal("schema cache missing synthetic-cluster cluster")
	}

	schema, ok := cluster.Tables["sample_db.synthetic_dist"]
	if !ok {
		t.Fatalf("schema cache missing sample_db.synthetic_dist; tables = %#v", cluster.Tables)
	}

	if schema.PartitionBy != "intDiv(sample_id, 32)" {
		t.Fatalf("PartitionBy = %q, want intDiv(sample_id, 32)", schema.PartitionBy)
	}

	if schema.OrderBy != "(sample_id, sample_value)" {
		t.Fatalf("OrderBy = %q, want (sample_id, sample_value)", schema.OrderBy)
	}

	if schema.PrimaryKey != "sample_id" {
		t.Fatalf("PrimaryKey = %q, want sample_id", schema.PrimaryKey)
	}

	if _, ok := cluster.Tables["sample_db.synthetic_dist_local"]; ok {
		t.Fatal("local backing table should not be exposed in the public schema listing")
	}
}
