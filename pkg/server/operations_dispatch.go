package server

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/ethpandaops/panda/pkg/proxy/handlers"
)

func (s *service) dispatchOperation(operationID string, w http.ResponseWriter, r *http.Request) bool {
	for _, handler := range []func(string, http.ResponseWriter, *http.Request) bool{
		s.handleClickHouseOperation,
		s.handlePrometheusOperation,
		s.handleLokiOperation,
		s.handleDoraOperation,
		s.handleForkyOperation,
		s.handleEthNodeOperation,
		s.handleCBTOperation,
		s.handleBenchmarkoorOperation,
		s.handleSpecsOperation,
		s.handleBlockArchiveOperation,
	} {
		if handler(operationID, w, r) {
			return true
		}
	}

	return false
}

func (s *service) proxyPassthroughGet(
	w http.ResponseWriter,
	r *http.Request,
	datasourceType string,
	path string,
	params url.Values,
	datasource string,
) {
	requestPath := path
	if len(params) > 0 {
		requestPath += "?" + params.Encode()
	}

	body, status, headers, err := s.proxyDatasourceRequest(
		r.Context(),
		datasourceType,
		datasource,
		http.MethodGet,
		requestPath,
		nil,
		http.Header{handlers.DatasourceHeader: []string{datasource}},
	)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	if status < 200 || status >= 300 {
		writeAPIError(w, status, strings.TrimSpace(string(body)))
		return
	}

	writePassthroughResponse(w, http.StatusOK, headers.Get("Content-Type"), body)
}
