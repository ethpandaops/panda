package server

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ethpandaops/panda/pkg/operations"
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
	items := make([]listItem, 0)
	for _, info := range s.proxyService.PrometheusDatasourceInfo() {
		items = append(items, listItem{
			Name:        info.Name,
			Description: info.Description,
			URL:         info.Metadata["url"],
			Type:        info.Type,
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
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	datasource, err := requiredStringArg(req.Args, "datasource")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	queryText, err := requiredStringArg(req.Args, "query")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	now := time.Now().UTC()
	params := url.Values{"query": {queryText}}
	path := "/prometheus/api/v1/query"

	if rangeQuery {
		startValue := optionalStringArg(req.Args, "start")
		if startValue == "" {
			startValue = "now-1h"
		}

		start, err := parsePrometheusTime(startValue, now)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}

		endValue := optionalStringArg(req.Args, "end")
		if endValue == "" {
			endValue = "now"
		}

		end, err := parsePrometheusTime(endValue, now)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}

		stepValue, err := requiredStringArg(req.Args, "step")
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}

		step, err := parseDurationSeconds(stepValue)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}

		params.Set("start", start)
		params.Set("end", end)
		params.Set("step", fmt.Sprintf("%d", step))
		path = "/prometheus/api/v1/query_range"
	} else if queryTime := optionalStringArg(req.Args, "time"); queryTime != "" {
		parsedTime, err := parsePrometheusTime(queryTime, now)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}

		params.Set("time", parsedTime)
	}

	s.proxyPassthroughGet(w, r, "prometheus", path, params, datasource)
}

func (s *service) handlePrometheusLabels(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	datasource, err := requiredStringArg(req.Args, "datasource")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	params, err := buildPrometheusLabelParams(req.Args)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.proxyPassthroughGet(w, r, "prometheus", "/prometheus/api/v1/labels", params, datasource)
}

func (s *service) handlePrometheusLabelValues(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	datasource, err := requiredStringArg(req.Args, "datasource")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	label, err := requiredStringArg(req.Args, "label")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	params, err := buildPrometheusLabelParams(req.Args)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.proxyPassthroughGet(w, r, "prometheus", "/prometheus/api/v1/label/"+url.PathEscape(label)+"/values", params, datasource)
}

func buildPrometheusLabelParams(args map[string]any) (url.Values, error) {
	params := url.Values{}
	now := time.Now().UTC()

	if start := optionalStringArg(args, "start"); start != "" {
		parsedStart, err := parsePrometheusTime(start, now)
		if err != nil {
			return nil, err
		}
		params.Set("start", parsedStart)
	}

	if end := optionalStringArg(args, "end"); end != "" {
		parsedEnd, err := parsePrometheusTime(end, now)
		if err != nil {
			return nil, err
		}
		params.Set("end", parsedEnd)
	}

	return params, nil
}

func parsePrometheusTime(value string, now time.Time) (string, error) {
	if value == "" {
		return "", nil
	}

	if value == "now" {
		return strconv.FormatInt(now.Unix(), 10), nil
	}

	if strings.HasPrefix(value, "now-") {
		seconds, err := parseDurationSeconds(strings.TrimPrefix(value, "now-"))
		if err != nil {
			return "", err
		}

		return strconv.FormatInt(now.Add(-time.Duration(seconds)*time.Second).Unix(), 10), nil
	}

	if intValue, err := strconv.ParseInt(value, 10, 64); err == nil {
		return strconv.FormatInt(intValue, 10), nil
	}

	if floatValue, err := strconv.ParseFloat(value, 64); err == nil {
		return strconv.FormatInt(int64(floatValue), 10), nil
	}

	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", fmt.Errorf("cannot parse time %q: %w", value, err)
	}

	return strconv.FormatInt(parsed.Unix(), 10), nil
}
