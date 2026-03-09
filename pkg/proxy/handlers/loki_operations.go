package handlers

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/mcp/pkg/operations"
)

type LokiOperationsHandler struct {
	log       logrus.FieldLogger
	instances map[string]*lokiOperationInstance
}

type lokiOperationInstance struct {
	cfg    LokiConfig
	client *http.Client
}

func NewLokiOperationsHandler(log logrus.FieldLogger, configs []LokiConfig) *LokiOperationsHandler {
	h := &LokiOperationsHandler{
		log:       log.WithField("handler", "loki-operations"),
		instances: make(map[string]*lokiOperationInstance, len(configs)),
	}

	for _, cfg := range configs {
		h.instances[cfg.Name] = &lokiOperationInstance{
			cfg: cfg,
			client: &http.Client{
				Timeout: time.Duration(cfg.Timeout) * time.Second,
				Transport: &http.Transport{
					TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.SkipVerify}, //nolint:gosec // user-configured
				},
			},
		}
	}

	return h
}

func (h *LokiOperationsHandler) HandleOperation(operationID string, w http.ResponseWriter, r *http.Request) bool {
	switch operationID {
	case "loki.list_datasources":
		h.handleListDatasources(w)
	case "loki.query":
		h.handleQuery(w, r, true)
	case "loki.query_instant":
		h.handleQuery(w, r, false)
	case "loki.get_labels":
		h.handleLabels(w, r)
	case "loki.get_label_values":
		h.handleLabelValues(w, r)
	default:
		return false
	}

	return true
}

func (h *LokiOperationsHandler) handleListDatasources(w http.ResponseWriter) {
	items := make([]map[string]any, 0, len(h.instances))
	for name, instance := range h.instances {
		items = append(items, map[string]any{
			"name":        name,
			"description": instance.cfg.Description,
			"url":         instance.cfg.URL,
		})
	}

	writeOperationResponse(h.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: map[string]any{"datasources": items},
	})
}

func (h *LokiOperationsHandler) handleQuery(w http.ResponseWriter, r *http.Request, rangeQuery bool) {
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

	logql, err := requiredStringArg(req.Args, "query")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	instance, ok := h.instances[datasource]
	if !ok {
		http.Error(w, fmt.Sprintf("unknown datasource: %s", datasource), http.StatusNotFound)
		return
	}

	params := url.Values{
		"query":     {logql},
		"limit":     {fmt.Sprintf("%d", optionalIntArg(req.Args, "limit", 100))},
		"direction": {optionalStringArg(req.Args, "direction")},
	}
	if params.Get("direction") == "" {
		params.Set("direction", "backward")
	}

	now := time.Now().UTC()
	path := "/loki/api/v1/query"

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
		path = "/loki/api/v1/query_range"
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

	body, contentType, status, err := h.executeAPIRequest(r.Context(), instance, path, params)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	writePassthroughResponse(w, http.StatusOK, contentType, body)
}

func (h *LokiOperationsHandler) handleLabels(w http.ResponseWriter, r *http.Request) {
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

	instance, ok := h.instances[datasource]
	if !ok {
		http.Error(w, fmt.Sprintf("unknown datasource: %s", datasource), http.StatusNotFound)
		return
	}

	params, err := h.buildLabelParams(req.Args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	body, contentType, status, err := h.executeAPIRequest(r.Context(), instance, "/loki/api/v1/labels", params)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	writePassthroughResponse(w, http.StatusOK, contentType, body)
}

func (h *LokiOperationsHandler) handleLabelValues(w http.ResponseWriter, r *http.Request) {
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

	instance, ok := h.instances[datasource]
	if !ok {
		http.Error(w, fmt.Sprintf("unknown datasource: %s", datasource), http.StatusNotFound)
		return
	}

	params, err := h.buildLabelParams(req.Args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	path := fmt.Sprintf("/loki/api/v1/label/%s/values", url.PathEscape(label))
	body, contentType, status, err := h.executeAPIRequest(r.Context(), instance, path, params)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	writePassthroughResponse(w, http.StatusOK, contentType, body)
}

func (h *LokiOperationsHandler) buildLabelParams(args map[string]any) (url.Values, error) {
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

func (h *LokiOperationsHandler) executeAPIRequest(
	ctx context.Context,
	instance *lokiOperationInstance,
	path string,
	params url.Values,
) ([]byte, string, int, error) {
	requestURL := strings.TrimRight(instance.cfg.URL, "/") + path
	if len(params) > 0 {
		requestURL += "?" + params.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, "", http.StatusInternalServerError, fmt.Errorf("creating Loki request: %w", err)
	}

	if instance.cfg.Username != "" {
		req.SetBasicAuth(instance.cfg.Username, instance.cfg.Password)
	}

	resp, err := instance.client.Do(req)
	if err != nil {
		return nil, "", http.StatusBadGateway, fmt.Errorf("executing Loki request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", http.StatusBadGateway, fmt.Errorf("reading Loki response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", resp.StatusCode, fmt.Errorf("%s", strings.TrimSpace(string(body)))
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}

	return body, contentType, http.StatusOK, nil
}
