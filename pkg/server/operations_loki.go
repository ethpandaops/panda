package server

import (
	"fmt"
	"net/http"
	"net/url"
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
	items := make([]map[string]any, 0)
	for _, info := range s.proxyService.LokiDatasourceInfo() {
		items = append(items, map[string]any{
			"name":        info.Name,
			"description": info.Description,
			"url":         info.Metadata["url"],
		})
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: map[string]any{"datasources": items},
	})
}

func (s *service) handleLokiQuery(w http.ResponseWriter, r *http.Request, rangeQuery bool) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	datasource, err := requiredStringArg(req.Args, "datasource")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	logQL, err := requiredStringArg(req.Args, "query")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	params := url.Values{
		"query":     {logQL},
		"limit":     {fmt.Sprintf("%d", optionalIntArg(req.Args, "limit", 100))},
		"direction": {optionalStringArg(req.Args, "direction")},
	}
	if params.Get("direction") == "" {
		params.Set("direction", "backward")
	}

	now := time.Now().UTC()
	path := "/loki/loki/api/v1/query"

	if rangeQuery {
		start := optionalStringArg(req.Args, "start")
		if start == "" {
			start = "now-1h"
		}

		end := optionalStringArg(req.Args, "end")
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
		queryTime := optionalStringArg(req.Args, "time")
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
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	datasource, err := requiredStringArg(req.Args, "datasource")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	params, err := buildLokiLabelParams(req.Args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.proxyPassthroughGet(w, r, "loki.get_labels", "/loki/loki/api/v1/labels", params, datasource)
}

func (s *service) handleLokiLabelValues(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	datasource, err := requiredStringArg(req.Args, "datasource")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	label, err := requiredStringArg(req.Args, "label")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	params, err := buildLokiLabelParams(req.Args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.proxyPassthroughGet(
		w,
		r,
		"loki.get_label_values",
		"/loki/loki/api/v1/label/"+url.PathEscape(label)+"/values",
		params,
		datasource,
	)
}

func buildLokiLabelParams(args map[string]any) (url.Values, error) {
	params := url.Values{}
	now := time.Now().UTC()

	if start := optionalStringArg(args, "start"); start != "" {
		parsedStart, err := parseLokiTime(start, now)
		if err != nil {
			return nil, err
		}
		params.Set("start", parsedStart)
	}

	if end := optionalStringArg(args, "end"); end != "" {
		parsedEnd, err := parseLokiTime(end, now)
		if err != nil {
			return nil, err
		}
		params.Set("end", parsedEnd)
	}

	return params, nil
}
