package server

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/ethpandaops/panda/pkg/operations"
	"github.com/ethpandaops/panda/pkg/proxy"
)

func (s *service) registerClickHouseOperations() {
	s.registerOperation("clickhouse.list_datasources", func(w http.ResponseWriter, _ *http.Request) {
		s.handleClickHouseListDatasources(w)
	})
	s.registerOperation("clickhouse.query", s.handleClickHouseQuery)
	s.registerOperation("clickhouse.query_raw", s.handleClickHouseQueryRaw)
}

func (s *service) handleClickHouseListDatasources(w http.ResponseWriter) {
	items := make([]operations.Datasource, 0)
	for _, info := range s.proxyService.ClickHouseDatasourceInfo() {
		items = append(items, operations.Datasource{
			Name:        info.Name,
			Description: info.Description,
			Database:    info.Metadata["database"],
		})
	}

	writeObjectOperationResponse(
		s.log,
		w,
		http.StatusOK,
		operations.DatasourcesPayload{Datasources: items},
		nil,
	)
}

func (s *service) handleClickHouseQuery(w http.ResponseWriter, r *http.Request) {
	s.handleClickHouseQueryOperation(w, r, "clickhouse.query")
}

func (s *service) handleClickHouseQueryRaw(w http.ResponseWriter, r *http.Request) {
	s.handleClickHouseQueryOperation(w, r, "clickhouse.query_raw")
}

func (s *service) handleClickHouseQueryOperation(w http.ResponseWriter, r *http.Request, operationID string) {
	request, err := decodeTypedOperationArgs[operations.ClickHouseQueryArgs](r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	datasource, err := requiredOneOfStringArg(map[string]any{
		"datasource": request.Datasource,
		"cluster":    request.Cluster,
	}, "datasource", "cluster")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if strings.TrimSpace(request.SQL) == "" {
		http.Error(w, "sql is required", http.StatusBadRequest)
		return
	}

	params := url.Values{"default_format": {"TabSeparatedWithNames"}}
	for key, value := range request.Parameters {
		params.Set("param_"+key, formatClickHouseParamValue(value))
	}

	body, status, headers, err := s.proxyRequest(
		r.Context(),
		http.MethodPost,
		"/clickhouse/?"+params.Encode(),
		strings.NewReader(request.SQL),
		http.Header{
			proxy.DatasourceHeader: []string{datasource},
			"Content-Type":         []string{"text/plain"},
		},
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
