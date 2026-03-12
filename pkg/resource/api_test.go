package resource

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/serverapi"
	"github.com/ethpandaops/panda/pkg/types"
)

func TestCreateAPIHandlerIncludesStorageModule(t *testing.T) {
	moduleReg := newInitializedModuleRegistry(t, &testModule{
		name: "docs",
		docs: map[string]types.ModuleDoc{
			"clickhouse": {Description: "Query ClickHouse"},
		},
	})

	payload, err := createAPIHandler(moduleReg)(context.Background(), "python://ethpandaops")
	require.NoError(t, err)

	var response serverapi.APIDocResponse
	require.NoError(t, json.Unmarshal([]byte(payload), &response))
	assert.Equal(t, "ethpandaops", response.Library)
	assert.Contains(t, response.Modules, "clickhouse")
	assert.Contains(t, response.Modules, "storage")
	assert.Contains(t, response.Modules["storage"].Functions, "upload")
}

func TestRegisterAPIResourcesRegistersStaticHandler(t *testing.T) {
	moduleReg := newInitializedModuleRegistry(t, &testModule{name: "docs", docs: map[string]types.ModuleDoc{}})
	reg := NewRegistry(logrus.New())

	RegisterAPIResources(logrus.New(), reg, moduleReg)

	static := reg.ListStatic()
	require.Len(t, static, 1)
	assert.Equal(t, "python://ethpandaops", static[0].URI)
}
