package resource

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/types"
)

func TestCreateDatasourcesHandlerFiltersByType(t *testing.T) {
	proxySvc := &testProxyService{
		infos: []types.DatasourceInfo{
			{Type: "clickhouse", Name: "xatu"},
			{Type: "loki", Name: "logs"},
		},
	}

	allPayload, err := createDatasourcesHandler(proxySvc, "")(context.Background(), "datasources://list")
	require.NoError(t, err)

	var all DatasourcesJSONResponse
	require.NoError(t, json.Unmarshal([]byte(allPayload), &all))
	require.Len(t, all.Datasources, 2)

	clickhousePayload, err := createDatasourcesHandler(proxySvc, "clickhouse")(context.Background(), "datasources://clickhouse")
	require.NoError(t, err)

	var clickhouse DatasourcesJSONResponse
	require.NoError(t, json.Unmarshal([]byte(clickhousePayload), &clickhouse))
	require.Len(t, clickhouse.Datasources, 1)
	assert.Equal(t, "xatu", clickhouse.Datasources[0].Name)
}

func TestRegisterDatasourcesResourcesRegistersAllResourceVariants(t *testing.T) {
	reg := NewRegistry(logrus.New())
	RegisterDatasourcesResources(logrus.New(), reg, nil)

	static := reg.ListStatic()
	require.Len(t, static, 4)
	assert.Equal(t, "datasources://list", static[0].URI)

	content, mimeType, err := reg.Read(context.Background(), "datasources://list")
	require.NoError(t, err)
	assert.Equal(t, "application/json", mimeType)
	assert.Contains(t, content, `"datasources": []`)
}
