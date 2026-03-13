package cli

import (
	"context"

	"github.com/ethpandaops/panda/pkg/operations"
)

func listLokiDatasources() ([]operations.Datasource, error) {
	response, err := serverOperationJSON[operations.NoArgs, operations.DatasourcesPayload](
		context.Background(),
		"loki.list_datasources",
		operations.NoArgs{},
	)
	if err != nil {
		return nil, err
	}

	return response.Datasources, nil
}

func lokiQuery(args operations.LokiQueryArgs) (*rawServerResponse, error) {
	return serverOperationRaw(context.Background(), "loki.query", args)
}

func lokiInstantQuery(args operations.LokiInstantQueryArgs) (*rawServerResponse, error) {
	return serverOperationRaw(context.Background(), "loki.query_instant", args)
}

func lokiLabels(args operations.LokiLabelsArgs) (*rawServerResponse, error) {
	return serverOperationRaw(context.Background(), "loki.get_labels", args)
}

func lokiLabelValues(args operations.LokiLabelValuesArgs) (*rawServerResponse, error) {
	return serverOperationRaw(context.Background(), "loki.get_label_values", args)
}
