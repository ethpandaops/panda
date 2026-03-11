package server

import (
	"net/http"
	"net/url"

	"github.com/ethpandaops/panda/pkg/proxy/handlers"
)

type operationHandler func(http.ResponseWriter, *http.Request)

func (s *service) registerOperation(operationID string, handler operationHandler) {
	s.operationHandlers[operationID] = handler
}

func (s *service) registerOperations() {
	s.registerClickHouseOperations()
	s.registerPrometheusOperations()
	s.registerLokiOperations()
	s.registerDoraOperations()
	s.registerEthNodeOperations()
}

func (s *service) dispatchOperation(operationID string, w http.ResponseWriter, r *http.Request) bool {
	handler, ok := s.operationHandlers[operationID]
	if !ok {
		return false
	}

	handler(w, r)

	return true
}

func (s *service) proxyPassthroughGet(
	w http.ResponseWriter,
	r *http.Request,
	operationID string,
	path string,
	params url.Values,
	datasource string,
) {
	requestPath := path
	if len(params) > 0 {
		requestPath += "?" + params.Encode()
	}

	body, status, headers, err := s.proxyRequest(
		r.Context(),
		http.MethodGet,
		requestPath,
		nil,
		http.Header{handlers.DatasourceHeader: []string{datasource}},
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	if status < 200 || status >= 300 {
		http.Error(w, upstreamFailureMessage(operationID, status, body, "datasource="+datasource), status)
		return
	}

	writePassthroughResponse(w, http.StatusOK, headers.Get("Content-Type"), body)
}
