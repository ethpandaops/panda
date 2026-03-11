package server

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ethpandaops/panda/pkg/operations"
)

func (s *service) registerPrometheusOperations() {
	s.registerOperation("prometheus.list_datasources", func(w http.ResponseWriter, _ *http.Request) {
		s.handlePrometheusListDatasources(w)
	})
	s.registerOperation("prometheus.query", func(w http.ResponseWriter, r *http.Request) {
		s.handlePrometheusQuery(w, r, false)
	})
	s.registerOperation("prometheus.query_range", func(w http.ResponseWriter, r *http.Request) {
		s.handlePrometheusQuery(w, r, true)
	})
	s.registerOperation("prometheus.get_labels", s.handlePrometheusLabels)
	s.registerOperation("prometheus.get_label_values", s.handlePrometheusLabelValues)
}

func (s *service) handlePrometheusListDatasources(w http.ResponseWriter) {
	items := make([]operations.Datasource, 0)
	for _, info := range s.proxyService.PrometheusDatasourceInfo() {
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

func (s *service) handlePrometheusQuery(w http.ResponseWriter, r *http.Request, rangeQuery bool) {
	now := time.Now().UTC()
	params := url.Values{}
	path := "/prometheus/api/v1/query"
	datasource := ""

	if rangeQuery {
		request, err := decodeTypedOperationArgs[operations.PrometheusRangeQueryArgs](r)
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

		if strings.TrimSpace(request.Step) == "" {
			http.Error(w, "step is required", http.StatusBadRequest)
			return
		}

		start, err := parsePrometheusTime(request.Start, now)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		end, err := parsePrometheusTime(request.End, now)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		step, err := parseDurationSeconds(request.Step)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		datasource = request.Datasource
		params.Set("query", request.Query)
		params.Set("start", start)
		params.Set("end", end)
		params.Set("step", fmt.Sprintf("%d", step))
		path = "/prometheus/api/v1/query_range"
	} else {
		request, err := decodeTypedOperationArgs[operations.PrometheusQueryArgs](r)
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
		params.Set("query", request.Query)
		if request.Time != "" {
			parsedTime, err := parsePrometheusTime(request.Time, now)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			params.Set("time", parsedTime)
		}
	}

	operationID := "prometheus.query"
	if rangeQuery {
		operationID = "prometheus.query_range"
	}

	s.proxyPassthroughGet(w, r, operationID, path, params, datasource)
}

func (s *service) handlePrometheusLabels(w http.ResponseWriter, r *http.Request) {
	request, err := decodeTypedOperationArgs[operations.DatasourceArgs](r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if strings.TrimSpace(request.Datasource) == "" {
		http.Error(w, "datasource is required", http.StatusBadRequest)
		return
	}

	s.proxyPassthroughGet(w, r, "prometheus.get_labels", "/prometheus/api/v1/labels", nil, request.Datasource)
}

func (s *service) handlePrometheusLabelValues(w http.ResponseWriter, r *http.Request) {
	request, err := decodeTypedOperationArgs[operations.DatasourceLabelArgs](r)
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

	s.proxyPassthroughGet(
		w,
		r,
		"prometheus.get_label_values",
		"/prometheus/api/v1/label/"+url.PathEscape(request.Label)+"/values",
		nil,
		request.Datasource,
	)
}
