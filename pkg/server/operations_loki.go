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

func (s *service) handleLokiOperation(operationID string, w http.ResponseWriter, r *http.Request) bool {
	switch operationID {
	case "loki.list_datasources":
		s.handleLokiListDatasources(w)
	case "loki.query":
		s.handleLokiQuery(w, r, true)
	case "loki.query_instant":
		s.handleLokiQuery(w, r, false)
	case "loki.get_labels":
		s.handleLokiLabels(w, r)
	case "loki.get_label_values":
		s.handleLokiLabelValues(w, r)
	default:
		return false
	}

	return true
}

func (s *service) handleLokiListDatasources(w http.ResponseWriter) {
	items := make([]listItem, 0)
	for _, info := range s.proxyService.LokiDatasourceInfo() {
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

func (s *service) handleLokiQuery(w http.ResponseWriter, r *http.Request, rangeQuery bool) {
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

	logQL, err := requiredStringArg(req.Args, "query")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
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
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}

		parsedEnd, err := parseLokiTime(end, now)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
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
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}

		params.Set("time", parsedTime)
	}

	s.proxyPassthroughGet(w, r, "loki", path, params, datasource)
}

func (s *service) handleLokiLabels(w http.ResponseWriter, r *http.Request) {
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

	params, err := buildLokiLabelParams(req.Args)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.proxyPassthroughGet(w, r, "loki", "/loki/loki/api/v1/labels", params, datasource)
}

func (s *service) handleLokiLabelValues(w http.ResponseWriter, r *http.Request) {
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

	params, err := buildLokiLabelParams(req.Args)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.proxyPassthroughGet(w, r, "loki", "/loki/loki/api/v1/label/"+url.PathEscape(label)+"/values", params, datasource)
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

func parseLokiTime(value string, now time.Time) (string, error) {
	if value == "" {
		return "", nil
	}

	if value == "now" {
		return strconv.FormatInt(now.UnixNano(), 10), nil
	}

	if strings.HasPrefix(value, "now-") {
		seconds, err := parseDurationSeconds(strings.TrimPrefix(value, "now-"))
		if err != nil {
			return "", err
		}

		return strconv.FormatInt(now.Add(-time.Duration(seconds)*time.Second).UnixNano(), 10), nil
	}

	if intValue, err := strconv.ParseInt(value, 10, 64); err == nil {
		if intValue < 1_000_000_000_000 {
			return strconv.FormatInt(intValue*1_000_000_000, 10), nil
		}

		return strconv.FormatInt(intValue, 10), nil
	}

	if floatValue, err := strconv.ParseFloat(value, 64); err == nil {
		return strconv.FormatInt(int64(floatValue*1_000_000_000), 10), nil
	}

	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", fmt.Errorf("cannot parse time %q: %w", value, err)
	}

	return strconv.FormatInt(parsed.UnixNano(), 10), nil
}
