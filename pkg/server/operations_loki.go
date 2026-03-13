package server

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ethpandaops/panda/pkg/operations"
	"github.com/ethpandaops/panda/pkg/proxy"
)

func (s *service) registerLokiOperations() {
	s.registerOperation("loki.list_datasources", func(w http.ResponseWriter, _ *http.Request) {
		s.handleLokiListDatasources(w)
	})
	s.registerOperation("loki.query", s.handleLokiRangeQuery)
	s.registerOperation("loki.query_instant", s.handleLokiInstantQuery)
	s.registerOperation("loki.get_labels", s.handleLokiLabels)
	s.registerOperation("loki.get_label_values", s.handleLokiLabelValues)
}

func (s *service) handleLokiListDatasources(w http.ResponseWriter) {
	items := make([]operations.Datasource, 0)
	for _, info := range proxy.FilterDatasourceInfoByType(s.proxyService.Datasources().Datasources, "loki") {
		items = append(items, operations.Datasource{
			Name:        info.Name,
			Description: info.Description,
			URL:         info.Metadata["url"],
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

type lokiProxyQuery struct {
	operationID string
	path        string
	datasource  string
	params      url.Values
}

func (s *service) handleLokiRangeQuery(w http.ResponseWriter, r *http.Request) {
	query, err := parseLokiRangeQuery(r, time.Now().UTC())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.proxyPassthroughGet(w, r, query.operationID, query.path, query.params, query.datasource)
}

func (s *service) handleLokiInstantQuery(w http.ResponseWriter, r *http.Request) {
	query, err := parseLokiInstantQuery(r, time.Now().UTC())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.proxyPassthroughGet(w, r, query.operationID, query.path, query.params, query.datasource)
}

func parseLokiRangeQuery(r *http.Request, now time.Time) (lokiProxyQuery, error) {
	request, err := decodeTypedOperationArgs[operations.LokiQueryArgs](r)
	if err != nil {
		return lokiProxyQuery{}, err
	}

	query, err := newLokiProxyQuery(
		"loki.query",
		"/loki/loki/api/v1/query_range",
		request.Datasource,
		request.Query,
		request.Direction,
		request.Limit,
	)
	if err != nil {
		return lokiProxyQuery{}, err
	}

	start := request.Start
	if start == "" {
		start = "now-1h"
	}

	end := request.End
	if end == "" {
		end = "now"
	}

	parsedStart, err := parseLokiTime(start, now)
	if err != nil {
		return lokiProxyQuery{}, err
	}

	parsedEnd, err := parseLokiTime(end, now)
	if err != nil {
		return lokiProxyQuery{}, err
	}

	query.params.Set("start", parsedStart)
	query.params.Set("end", parsedEnd)

	return query, nil
}

func parseLokiInstantQuery(r *http.Request, now time.Time) (lokiProxyQuery, error) {
	request, err := decodeTypedOperationArgs[operations.LokiInstantQueryArgs](r)
	if err != nil {
		return lokiProxyQuery{}, err
	}

	query, err := newLokiProxyQuery(
		"loki.query_instant",
		"/loki/loki/api/v1/query",
		request.Datasource,
		request.Query,
		request.Direction,
		request.Limit,
	)
	if err != nil {
		return lokiProxyQuery{}, err
	}

	queryTime := request.Time
	if queryTime == "" {
		queryTime = "now"
	}

	parsedTime, err := parseLokiTime(queryTime, now)
	if err != nil {
		return lokiProxyQuery{}, err
	}

	query.params.Set("time", parsedTime)

	return query, nil
}

func newLokiProxyQuery(
	operationID, path, datasource, queryText, direction string,
	limit int,
) (lokiProxyQuery, error) {
	datasource = strings.TrimSpace(datasource)
	if datasource == "" {
		return lokiProxyQuery{}, fmt.Errorf("datasource is required")
	}

	queryText = strings.TrimSpace(queryText)
	if queryText == "" {
		return lokiProxyQuery{}, fmt.Errorf("query is required")
	}

	direction = strings.TrimSpace(direction)
	if direction == "" {
		direction = "backward"
	}

	if limit <= 0 {
		limit = 100
	}

	params := url.Values{}
	params.Set("query", queryText)
	params.Set("limit", fmt.Sprintf("%d", limit))
	params.Set("direction", direction)

	return lokiProxyQuery{
		operationID: operationID,
		path:        path,
		datasource:  datasource,
		params:      params,
	}, nil
}

func (s *service) handleLokiLabels(w http.ResponseWriter, r *http.Request) {
	request, err := decodeTypedOperationArgs[operations.LokiLabelsArgs](r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if strings.TrimSpace(request.Datasource) == "" {
		http.Error(w, "datasource is required", http.StatusBadRequest)
		return
	}

	params, err := buildLokiLabelParams(request.Start, request.End)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.proxyPassthroughGet(w, r, "loki.get_labels", "/loki/loki/api/v1/labels", params, request.Datasource)
}

func (s *service) handleLokiLabelValues(w http.ResponseWriter, r *http.Request) {
	request, err := decodeTypedOperationArgs[operations.LokiLabelValuesArgs](r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if strings.TrimSpace(request.Datasource) == "" {
		http.Error(w, "datasource is required", http.StatusBadRequest)
		return
	}

	if strings.TrimSpace(request.Label) == "" {
		http.Error(w, "label is required", http.StatusBadRequest)
		return
	}

	params, err := buildLokiLabelParams(request.Start, request.End)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.proxyPassthroughGet(
		w,
		r,
		"loki.get_label_values",
		"/loki/loki/api/v1/label/"+url.PathEscape(request.Label)+"/values",
		params,
		request.Datasource,
	)
}

func buildLokiLabelParams(start, end string) (url.Values, error) {
	params := url.Values{}
	now := time.Now().UTC()

	if start != "" {
		parsedStart, err := parseLokiTime(start, now)
		if err != nil {
			return nil, err
		}
		params.Set("start", parsedStart)
	}

	if end != "" {
		parsedEnd, err := parseLokiTime(end, now)
		if err != nil {
			return nil, err
		}
		params.Set("end", parsedEnd)
	}

	return params, nil
}
