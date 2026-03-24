package server

import (
	"net/http"
	"net/url"
	"strings"
)

const proxyDatasourceHeader = "X-Datasource"

func (s *service) dispatchOperation(operationID string, w http.ResponseWriter, r *http.Request) bool {
	for _, handler := range []func(string, http.ResponseWriter, *http.Request) bool{
		s.handleClickHouseOperation,
		s.handlePrometheusOperation,
		s.handleLokiOperation,
		s.handleDoraOperation,
		s.handleEthNodeOperation,
		s.handleCBTOperation,
		s.handleSpecsOperation,
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
		http.Header{proxyDatasourceHeader: []string{datasource}},
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	if status < 200 || status >= 300 {
		http.Error(w, strings.TrimSpace(string(body)), status)
		return
	}

	writePassthroughResponse(w, http.StatusOK, headers.Get("Content-Type"), body)
}
