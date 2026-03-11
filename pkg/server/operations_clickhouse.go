package server

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/ethpandaops/panda/pkg/operations"
	"github.com/ethpandaops/panda/pkg/proxy/handlers"
)

func (s *service) registerClickHouseOperations() {
	s.registerOperation("clickhouse.list_datasources", func(w http.ResponseWriter, _ *http.Request) {
		s.handleClickHouseListDatasources(w)
	})
	s.registerOperation("clickhouse.query", s.handleClickHouseQuery)
	s.registerOperation("clickhouse.query_raw", s.handleClickHouseQuery)
}

func (s *service) handleClickHouseListDatasources(w http.ResponseWriter) {
	items := make([]map[string]any, 0)
	for _, info := range s.proxyService.ClickHouseDatasourceInfo() {
		items = append(items, map[string]any{
			"name":        info.Name,
			"description": info.Description,
			"database":    info.Metadata["database"],
		})
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: map[string]any{"datasources": items},
	})
}

func (s *service) handleClickHouseQuery(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	datasourceName, err := requiredOneOfStringArg(req.Args, "datasource", "cluster")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	sql, err := requiredStringArg(req.Args, "sql")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	params := url.Values{"default_format": {"TabSeparatedWithNames"}}
	for key, value := range optionalMapArg(req.Args, "parameters") {
		params.Set("param_"+key, formatClickHouseParamValue(value))
	}

	body, status, headers, err := s.proxyRequest(
		r.Context(),
		http.MethodPost,
		"/clickhouse/?"+params.Encode(),
		strings.NewReader(sql),
		http.Header{
			handlers.DatasourceHeader: []string{datasourceName},
			"Content-Type":            []string{"text/plain"},
		},
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	if status < 200 || status >= 300 {
		http.Error(w, upstreamFailureMessage("clickhouse.query", status, body, "datasource="+datasourceName), status)
		return
	}

	writePassthroughResponse(w, http.StatusOK, headers.Get("Content-Type"), body)
}

func formatClickHouseParamValue(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case bool:
		if v {
			return "1"
		}
		return "0"
	default:
		return fmt.Sprint(v)
	}
}
