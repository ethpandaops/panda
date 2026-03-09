package handlers

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/mcp/pkg/operations"
)

type ClickHouseOperationsHandler struct {
	log      logrus.FieldLogger
	clusters map[string]*clickhouseOperationCluster
}

type clickhouseOperationCluster struct {
	cfg     ClickHouseConfig
	baseURL string
	client  *http.Client
}

type clickhouseResponseMeta struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type clickhouseJSONResponse struct {
	Meta       []clickhouseResponseMeta `json:"meta"`
	Data       []map[string]any         `json:"data"`
	Rows       int                      `json:"rows"`
	RowsBefore int                      `json:"rows_before_limit_at_least"`
	Statistics map[string]any           `json:"statistics"`
}

type clickhouseJSONCompactResponse struct {
	Meta       []clickhouseResponseMeta `json:"meta"`
	Data       [][]any                  `json:"data"`
	Rows       int                      `json:"rows"`
	RowsBefore int                      `json:"rows_before_limit_at_least"`
	Statistics map[string]any           `json:"statistics"`
}

func NewClickHouseOperationsHandler(log logrus.FieldLogger, configs []ClickHouseConfig) *ClickHouseOperationsHandler {
	h := &ClickHouseOperationsHandler{
		log:      log.WithField("handler", "clickhouse-operations"),
		clusters: make(map[string]*clickhouseOperationCluster, len(configs)),
	}

	for _, cfg := range configs {
		scheme := "https"
		if !cfg.Secure {
			scheme = "http"
		}

		h.clusters[cfg.Name] = &clickhouseOperationCluster{
			cfg:     cfg,
			baseURL: fmt.Sprintf("%s://%s:%d", scheme, cfg.Host, cfg.Port),
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

func (h *ClickHouseOperationsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch strings.TrimPrefix(r.URL.Path, "/api/v1/operations/") {
	case "clickhouse.list_datasources":
		h.handleListDatasources(w)
	case "clickhouse.query":
		h.handleQuery(w, r, false)
	case "clickhouse.query_raw":
		h.handleQuery(w, r, true)
	default:
		http.NotFound(w, r)
	}
}

func (h *ClickHouseOperationsHandler) handleListDatasources(w http.ResponseWriter) {
	items := make([]map[string]any, 0, len(h.clusters))
	for name, cluster := range h.clusters {
		items = append(items, map[string]any{
			"name":        name,
			"description": cluster.cfg.Description,
			"database":    cluster.cfg.Database,
		})
	}

	h.writeJSON(w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: map[string]any{
			"datasources": items,
		},
	})
}

func (h *ClickHouseOperationsHandler) handleQuery(w http.ResponseWriter, r *http.Request, raw bool) {
	var req operations.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	clusterName, _ := req.Args["cluster"].(string)
	sql, _ := req.Args["sql"].(string)
	if clusterName == "" {
		http.Error(w, "cluster is required", http.StatusBadRequest)
		return
	}
	if sql == "" {
		http.Error(w, "sql is required", http.StatusBadRequest)
		return
	}

	cluster, ok := h.clusters[clusterName]
	if !ok {
		http.Error(w, fmt.Sprintf("unknown cluster: %s", clusterName), http.StatusNotFound)
		return
	}

	argsParams := map[string]any{}
	if rawParams, ok := req.Args["parameters"].(map[string]any); ok {
		argsParams = rawParams
	}

	if raw {
		resp, status, err := h.executeJSONCompactQuery(r.Context(), cluster, sql, argsParams)
		if err != nil {
			http.Error(w, err.Error(), status)
			return
		}

		columns := make([]string, 0, len(resp.Meta))
		for _, meta := range resp.Meta {
			columns = append(columns, meta.Name)
		}

		h.writeJSON(w, http.StatusOK, operations.Response{
			Kind:        operations.ResultKindTable,
			RowEncoding: operations.RowEncodingArray,
			Columns:     columns,
			Matrix:      resp.Data,
			Meta: map[string]any{
				"cluster":                    clusterName,
				"rows":                       resp.Rows,
				"rows_before_limit_at_least": resp.RowsBefore,
				"statistics":                 resp.Statistics,
			},
		})

		return
	}

	resp, status, err := h.executeJSONQuery(r.Context(), cluster, sql, argsParams)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	columns := make([]string, 0, len(resp.Meta))
	for _, meta := range resp.Meta {
		columns = append(columns, meta.Name)
	}

	h.writeJSON(w, http.StatusOK, operations.Response{
		Kind:        operations.ResultKindTable,
		RowEncoding: operations.RowEncodingObject,
		Columns:     columns,
		Rows:        resp.Data,
		Meta: map[string]any{
			"cluster":                    clusterName,
			"rows":                       resp.Rows,
			"rows_before_limit_at_least": resp.RowsBefore,
			"statistics":                 resp.Statistics,
		},
	})
}

func (h *ClickHouseOperationsHandler) executeJSONQuery(
	ctx context.Context,
	cluster *clickhouseOperationCluster,
	sql string,
	parameters map[string]any,
) (*clickhouseJSONResponse, int, error) {
	body, status, err := h.executeQuery(ctx, cluster, sql, "JSON", parameters)
	if err != nil {
		return nil, status, err
	}

	var resp clickhouseJSONResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("invalid ClickHouse JSON response: %w", err)
	}

	return &resp, http.StatusOK, nil
}

func (h *ClickHouseOperationsHandler) executeJSONCompactQuery(
	ctx context.Context,
	cluster *clickhouseOperationCluster,
	sql string,
	parameters map[string]any,
) (*clickhouseJSONCompactResponse, int, error) {
	body, status, err := h.executeQuery(ctx, cluster, sql, "JSONCompact", parameters)
	if err != nil {
		return nil, status, err
	}

	var resp clickhouseJSONCompactResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("invalid ClickHouse JSONCompact response: %w", err)
	}

	return &resp, http.StatusOK, nil
}

func (h *ClickHouseOperationsHandler) executeQuery(
	ctx context.Context,
	cluster *clickhouseOperationCluster,
	sql, format string,
	parameters map[string]any,
) ([]byte, int, error) {
	params := url.Values{
		"default_format": []string{format},
	}
	if cluster.cfg.Database != "" {
		params.Set("database", cluster.cfg.Database)
	}

	for key, value := range parameters {
		params.Set("param_"+key, formatParamValue(value))
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		cluster.baseURL+"/?"+params.Encode(),
		bytes.NewBufferString(sql),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("creating ClickHouse request: %w", err)
	}

	if cluster.cfg.Username != "" {
		req.SetBasicAuth(cluster.cfg.Username, cluster.cfg.Password)
	}

	resp, err := cluster.client.Do(req)
	if err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("executing ClickHouse request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("reading ClickHouse response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, fmt.Errorf("%s", strings.TrimSpace(string(body)))
	}

	return body, http.StatusOK, nil
}

func (h *ClickHouseOperationsHandler) writeJSON(w http.ResponseWriter, status int, response operations.Response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		h.log.WithError(err).Error("Failed to encode operation response")
	}
}

func formatParamValue(value any) string {
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
