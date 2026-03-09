package handlers

import (
	"bytes"
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

type ClickHouseOperationsHandler struct {
	log      logrus.FieldLogger
	clusters map[string]*clickhouseOperationCluster
}

type clickhouseOperationCluster struct {
	cfg     ClickHouseConfig
	baseURL string
	client  *http.Client
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
	h.HandleOperation(strings.TrimPrefix(r.URL.Path, "/api/v1/operations/"), w, r)
}

func (h *ClickHouseOperationsHandler) HandleOperation(operationID string, w http.ResponseWriter, r *http.Request) bool {
	switch operationID {
	case "clickhouse.list_datasources":
		h.handleListDatasources(w)
	case "clickhouse.query":
		h.handleQuery(w, r)
	case "clickhouse.query_raw":
		h.handleQuery(w, r)
	default:
		return false
	}

	return true
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

	writeOperationResponse(h.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: map[string]any{
			"datasources": items,
		},
	})
}

func (h *ClickHouseOperationsHandler) handleQuery(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	clusterName, err := requiredStringArg(req.Args, "cluster")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	sql, err := requiredStringArg(req.Args, "sql")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	cluster, ok := h.clusters[clusterName]
	if !ok {
		http.Error(w, fmt.Sprintf("unknown cluster: %s", clusterName), http.StatusNotFound)
		return
	}

	body, contentType, status, err := h.executeQuery(
		r.Context(),
		cluster,
		sql,
		"TabSeparatedWithNames",
		optionalMapArg(req.Args, "parameters"),
	)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	writePassthroughResponse(w, http.StatusOK, contentType, body)
}

func (h *ClickHouseOperationsHandler) executeQuery(
	ctx context.Context,
	cluster *clickhouseOperationCluster,
	sql, format string,
	parameters map[string]any,
) ([]byte, string, int, error) {
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
		return nil, "", http.StatusInternalServerError, fmt.Errorf("creating ClickHouse request: %w", err)
	}

	if cluster.cfg.Username != "" {
		req.SetBasicAuth(cluster.cfg.Username, cluster.cfg.Password)
	}

	resp, err := cluster.client.Do(req)
	if err != nil {
		return nil, "", http.StatusBadGateway, fmt.Errorf("executing ClickHouse request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", http.StatusBadGateway, fmt.Errorf("reading ClickHouse response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", resp.StatusCode, fmt.Errorf("%s", strings.TrimSpace(string(body)))
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "text/tab-separated-values; charset=utf-8"
	}

	return body, contentType, http.StatusOK, nil
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
