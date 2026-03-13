package cli

import (
	"context"
	"encoding/json"
	"fmt"

	clickhousemodule "github.com/ethpandaops/panda/modules/clickhouse"
	"github.com/ethpandaops/panda/pkg/operations"
)

func listClickHouseDatasources() ([]operations.Datasource, error) {
	response, err := serverOperationJSON[operations.NoArgs, operations.DatasourcesPayload](
		context.Background(),
		"clickhouse.list_datasources",
		operations.NoArgs{},
	)
	if err != nil {
		return nil, err
	}

	return response.Datasources, nil
}

func clickHouseQuery(ctx context.Context, args operations.ClickHouseQueryArgs) (*rawServerResponse, error) {
	return serverOperationRaw(ctx, "clickhouse.query", args)
}

func clickHouseQueryRaw(ctx context.Context, args operations.ClickHouseQueryArgs) (*rawServerResponse, error) {
	return serverOperationRaw(ctx, "clickhouse.query_raw", args)
}

func readClickHouseTables(ctx context.Context) (*clickhousemodule.TablesListResponse, error) {
	response, err := readResource(ctx, "clickhouse://tables")
	if err != nil {
		return nil, err
	}

	var payload clickhousemodule.TablesListResponse
	if err := json.Unmarshal([]byte(response.Content), &payload); err != nil {
		return nil, fmt.Errorf("decoding tables list: %w", err)
	}

	return &payload, nil
}

func readClickHouseTable(ctx context.Context, tableName string) (*clickhousemodule.TableDetailResponse, error) {
	response, err := readResource(ctx, "clickhouse://tables/"+tableName)
	if err != nil {
		return nil, err
	}

	var payload clickhousemodule.TableDetailResponse
	if err := json.Unmarshal([]byte(response.Content), &payload); err != nil {
		return nil, fmt.Errorf("decoding table detail: %w", err)
	}

	return &payload, nil
}
