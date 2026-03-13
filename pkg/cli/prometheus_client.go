package cli

import (
	"context"

	"github.com/ethpandaops/panda/pkg/operations"
)

func listPrometheusDatasources() ([]operations.Datasource, error) {
	response, err := serverOperationJSON[operations.NoArgs, operations.DatasourcesPayload](
		context.Background(),
		"prometheus.list_datasources",
		operations.NoArgs{},
	)
	if err != nil {
		return nil, err
	}

	return response.Datasources, nil
}

func prometheusQuery(args operations.PrometheusQueryArgs) (*rawServerResponse, error) {
	return serverOperationRaw(context.Background(), "prometheus.query", args)
}

func prometheusQueryRange(args operations.PrometheusRangeQueryArgs) (*rawServerResponse, error) {
	return serverOperationRaw(context.Background(), "prometheus.query_range", args)
}

func prometheusLabels(args operations.DatasourceArgs) (*rawServerResponse, error) {
	return serverOperationRaw(context.Background(), "prometheus.get_labels", args)
}

func prometheusLabelValues(args operations.DatasourceLabelArgs) (*rawServerResponse, error) {
	return serverOperationRaw(context.Background(), "prometheus.get_label_values", args)
}
