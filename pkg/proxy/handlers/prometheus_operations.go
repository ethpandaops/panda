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

type PrometheusOperationsHandler struct {
	log       logrus.FieldLogger
	instances map[string]*prometheusOperationInstance
}

type prometheusOperationInstance struct {
	cfg    PrometheusConfig
	client *http.Client
}

func NewPrometheusOperationsHandler(log logrus.FieldLogger, configs []PrometheusConfig) *PrometheusOperationsHandler {
	h := &PrometheusOperationsHandler{
		log:       log.WithField("handler", "prometheus-operations"),
		instances: make(map[string]*prometheusOperationInstance, len(configs)),
	}

	for _, cfg := range configs {
		h.instances[cfg.Name] = &prometheusOperationInstance{
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

func (h *PrometheusOperationsHandler) HandleOperation(operationID string, w http.ResponseWriter, r *http.Request) bool {
	switch operationID {
	case "prometheus.list_datasources":
		h.handleListDatasources(w)
	case "prometheus.query":
		h.handleQuery(w, r, false)
	case "prometheus.query_range":
		h.handleQuery(w, r, true)
	case "prometheus.get_labels":
		h.handleLabels(w, r)
	case "prometheus.get_label_values":
		h.handleLabelValues(w, r)
	default:
		return false
	}

	return true
}

func (h *PrometheusOperationsHandler) handleListDatasources(w http.ResponseWriter) {
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

func (h *PrometheusOperationsHandler) handleQuery(w http.ResponseWriter, r *http.Request, rangeQuery bool) {
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

	query, err := requiredStringArg(req.Args, "query")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	instance, ok := h.instances[datasource]
	if !ok {
		http.Error(w, fmt.Sprintf("unknown datasource: %s", datasource), http.StatusNotFound)
		return
	}

	now := time.Now().UTC()
	params := url.Values{"query": {query}}
	path := "/api/v1/query"

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
		path = "/api/v1/query_range"
	} else if queryTime := optionalStringArg(req.Args, "time"); queryTime != "" {
		parsedTime, err := parsePrometheusTime(queryTime, now)
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

func (h *PrometheusOperationsHandler) handleLabels(w http.ResponseWriter, r *http.Request) {
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

	body, contentType, status, err := h.executeAPIRequest(r.Context(), instance, "/api/v1/labels", nil)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	writePassthroughResponse(w, http.StatusOK, contentType, body)
}

func (h *PrometheusOperationsHandler) handleLabelValues(w http.ResponseWriter, r *http.Request) {
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

	path := fmt.Sprintf("/api/v1/label/%s/values", url.PathEscape(label))
	body, contentType, status, err := h.executeAPIRequest(r.Context(), instance, path, nil)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	writePassthroughResponse(w, http.StatusOK, contentType, body)
}

func (h *PrometheusOperationsHandler) executeAPIRequest(
	ctx context.Context,
	instance *prometheusOperationInstance,
	path string,
	params url.Values,
) ([]byte, string, int, error) {
	requestURL := strings.TrimRight(instance.cfg.URL, "/") + path
	if len(params) > 0 {
		requestURL += "?" + params.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, "", http.StatusInternalServerError, fmt.Errorf("creating Prometheus request: %w", err)
	}

	if instance.cfg.Username != "" {
		req.SetBasicAuth(instance.cfg.Username, instance.cfg.Password)
	}

	resp, err := instance.client.Do(req)
	if err != nil {
		return nil, "", http.StatusBadGateway, fmt.Errorf("executing Prometheus request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", http.StatusBadGateway, fmt.Errorf("reading Prometheus response: %w", err)
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
