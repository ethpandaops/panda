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

func TestRegisterSchemaResourcesRegistersWorkingHandlers(t *testing.T) {
	client := &stubSchemaClient{
		clusters: map[string]*ClusterTables{
			"xatu": {
				ClusterName: "xatu",
				LastUpdated: time.Unix(123, 0).UTC(),
				Tables: map[string]*TableSchema{
					"blocks": {
						Name:          "blocks",
						Columns:       []TableColumn{{Name: "slot", Type: "UInt64"}},
						HasNetworkCol: true,
					},
				},
			},
		},
	}
	registry := &stubResourceRegistry{}

	RegisterSchemaResources(logrus.New(), registry, client)

	require.Len(t, registry.staticResources, 1)
	require.Len(t, registry.templateResources, 1)
	assert.True(t, registry.templateResources[0].Pattern.MatchString("clickhouse://tables/blocks"))

	listPayload, err := registry.staticResources[0].Handler(context.Background(), "clickhouse://tables")
	require.NoError(t, err)

	var list TablesListResponse
	require.NoError(t, json.Unmarshal([]byte(listPayload), &list))
	require.Contains(t, list.Clusters, "xatu")
	assert.Equal(t, 1, list.Clusters["xatu"].TableCount)

	detailPayload, err := registry.templateResources[0].Handler(context.Background(), "clickhouse://tables/blocks")
	require.NoError(t, err)

	var detail TableDetailResponse
	require.NoError(t, json.Unmarshal([]byte(detailPayload), &detail))
	require.NotNil(t, detail.Table)
	assert.Equal(t, "blocks", detail.Table.Name)
	assert.Equal(t, "xatu", detail.Cluster)
}

func TestExtractTableNameRejectsMismatchedPrefixes(t *testing.T) {
	assert.Equal(t, "blocks", extractTableName("clickhouse://tables/blocks"))
	assert.Empty(t, extractTableName("clickhouse://other/blocks"))
	assert.Empty(t, extractTableName("clickhouse://tables"))
}

func TestCreateTablesListHandlerReturnsEmptyPayload(t *testing.T) {
	payload, err := createTablesListHandler(&stubSchemaClient{clusters: map[string]*ClusterTables{}})(
		context.Background(),
		"clickhouse://tables",
	)
	require.NoError(t, err)

	var list TablesListResponse
	require.NoError(t, json.Unmarshal([]byte(payload), &list))
	assert.Empty(t, list.Clusters)
	assert.Contains(t, list.Usage, "clickhouse://tables/{table_name}")
}
