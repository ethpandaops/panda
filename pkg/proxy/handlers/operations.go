package handlers

import (
	"net/http"
	"strings"

	"github.com/sirupsen/logrus"
)

type OperationsHandler struct {
	log        logrus.FieldLogger
	clickhouse *ClickHouseOperationsHandler
	prometheus *PrometheusOperationsHandler
	loki       *LokiOperationsHandler
	ethnode    *EthNodeOperationsHandler
	dora       *DoraOperationsHandler
}

func NewOperationsHandler(
	log logrus.FieldLogger,
	clickhouseConfigs []ClickHouseConfig,
	prometheusConfigs []PrometheusConfig,
	lokiConfigs []LokiConfig,
	ethNodeConfig *EthNodeConfig,
) *OperationsHandler {
	handler := &OperationsHandler{
		log:  log.WithField("handler", "operations"),
		dora: NewDoraOperationsHandler(log),
	}

	if len(clickhouseConfigs) > 0 {
		handler.clickhouse = NewClickHouseOperationsHandler(log, clickhouseConfigs)
	}

	if len(prometheusConfigs) > 0 {
		handler.prometheus = NewPrometheusOperationsHandler(log, prometheusConfigs)
	}

	if len(lokiConfigs) > 0 {
		handler.loki = NewLokiOperationsHandler(log, lokiConfigs)
	}

	if ethNodeConfig != nil {
		handler.ethnode = NewEthNodeOperationsHandler(log, *ethNodeConfig)
	}

	return handler
}

func (h *OperationsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	operationID := strings.TrimPrefix(r.URL.Path, "/api/v1/operations/")

	for _, candidate := range []interface {
		HandleOperation(operationID string, w http.ResponseWriter, r *http.Request) bool
	}{
		h.clickhouse,
		h.prometheus,
		h.loki,
		h.ethnode,
		h.dora,
	} {
		if candidate != nil && candidate.HandleOperation(operationID, w, r) {
			return
		}
	}

	http.NotFound(w, r)
}
