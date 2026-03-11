package server

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ethpandaops/panda/pkg/operations"
)

func (s *service) registerLokiOperations() {
	s.registerOperation("loki.list_datasources", func(w http.ResponseWriter, _ *http.Request) {
		s.handleLokiListDatasources(w)
	})
	s.registerOperation("loki.query", func(w http.ResponseWriter, r *http.Request) {
		s.handleLokiQuery(w, r, true)
	})
	s.registerOperation("loki.query_instant", func(w http.ResponseWriter, r *http.Request) {
		s.handleLokiQuery(w, r, false)
	})
	s.registerOperation("loki.get_labels", s.handleLokiLabels)
	s.registerOperation("loki.get_label_values", s.handleLokiLabelValues)
}

func (s *service) handleLokiListDatasources(w http.ResponseWriter) {
	items := make([]operations.Datasource, 0)
	for _, info := range s.proxyService.LokiDatasourceInfo() {
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

func (s *service) handleLokiQuery(w http.ResponseWriter, r *http.Request, rangeQuery bool) {
	now := time.Now().UTC()
	params := url.Values{}
	path := "/loki/loki/api/v1/query"
	datasource := ""
	direction := "backward"

	if rangeQuery {
		request, err := decodeTypedOperationArgs[operations.LokiQueryArgs](r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if strings.TrimSpace(request.Datasource) == "" {
			http.Error(w, "datasource is required", http.StatusBadRequest)
			return
		}

		if strings.TrimSpace(request.Query) == "" {
			http.Error(w, "query is required", http.StatusBadRequest)
			return
		}

		datasource = request.Datasource
		direction = strings.TrimSpace(request.Direction)
		if direction == "" {
			direction = "backward"
		}
		limit := request.Limit
		if limit <= 0 {
			limit = 100
		}
		params.Set("query", request.Query)
		params.Set("limit", fmt.Sprintf("%d", limit))
		params.Set("direction", direction)

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
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		parsedEnd, err := parseLokiTime(end, now)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		params.Set("start", parsedStart)
		params.Set("end", parsedEnd)
		path = "/loki/loki/api/v1/query_range"
	} else {
		request, err := decodeTypedOperationArgs[operations.LokiInstantQueryArgs](r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if strings.TrimSpace(request.Datasource) == "" {
			http.Error(w, "datasource is required", http.StatusBadRequest)
			return
		}

		if strings.TrimSpace(request.Query) == "" {
			http.Error(w, "query is required", http.StatusBadRequest)
			return
		}

		datasource = request.Datasource
		direction = strings.TrimSpace(request.Direction)
		if direction == "" {
			direction = "backward"
		}
		limit := request.Limit
		if limit <= 0 {
			limit = 100
		}
		params.Set("query", request.Query)
		params.Set("limit", fmt.Sprintf("%d", limit))
		params.Set("direction", direction)

		queryTime := request.Time
		if queryTime == "" {
			queryTime = "now"
		}

		parsedTime, err := parseLokiTime(queryTime, now)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		params.Set("time", parsedTime)
	}

	operationID := "loki.query"
	if !rangeQuery {
		operationID = "loki.query_instant"
	}

	s.proxyPassthroughGet(w, r, operationID, path, params, datasource)
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
