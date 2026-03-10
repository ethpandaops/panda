package server

import (
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/ethpandaops/mcp/pkg/operations"
)

func (s *service) handlePrometheusOperation(operationID string, w http.ResponseWriter, r *http.Request) bool {
	switch operationID {
	case "prometheus.list_datasources":
		s.handlePrometheusListDatasources(w)
	case "prometheus.query":
		s.handlePrometheusQuery(w, r, false)
	case "prometheus.query_range":
		s.handlePrometheusQuery(w, r, true)
	case "prometheus.get_labels":
		s.handlePrometheusLabels(w, r)
	case "prometheus.get_label_values":
		s.handlePrometheusLabelValues(w, r)
	default:
		return false
	}

	return true
}

func (s *service) handlePrometheusListDatasources(w http.ResponseWriter) {
	items := make([]map[string]any, 0)
	for _, info := range s.proxyService.PrometheusDatasourceInfo() {
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

func (s *service) handlePrometheusQuery(w http.ResponseWriter, r *http.Request, rangeQuery bool) {
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

	queryText, err := requiredStringArg(req.Args, "query")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	now := time.Now().UTC()
	params := url.Values{"query": {queryText}}
	path := "/prometheus/api/v1/query"

	if rangeQuery {
		start, err := parsePrometheusTime(optionalStringArg(req.Args, "start"), now)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		end, err := parsePrometheusTime(optionalStringArg(req.Args, "end"), now)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		stepValue, err := requiredStringArg(req.Args, "step")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		step, err := parseDurationSeconds(stepValue)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		params.Set("start", start)
		params.Set("end", end)
		params.Set("step", fmt.Sprintf("%d", step))
		path = "/prometheus/api/v1/query_range"
	} else if queryTime := optionalStringArg(req.Args, "time"); queryTime != "" {
		parsedTime, err := parsePrometheusTime(queryTime, now)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		params.Set("time", parsedTime)
	}

	s.proxyPassthroughGet(w, r, path, params, datasource)
}

func (s *service) handlePrometheusLabels(w http.ResponseWriter, r *http.Request) {
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

	s.proxyPassthroughGet(w, r, "/prometheus/api/v1/labels", nil, datasource)
}

func (s *service) handlePrometheusLabelValues(w http.ResponseWriter, r *http.Request) {
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

	s.proxyPassthroughGet(w, r, "/prometheus/api/v1/label/"+url.PathEscape(label)+"/values", nil, datasource)
}
