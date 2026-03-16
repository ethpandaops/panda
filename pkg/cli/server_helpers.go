package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"syscall"

	clickhousemodule "github.com/ethpandaops/panda/modules/clickhouse"
	"github.com/ethpandaops/panda/pkg/config"
	"github.com/ethpandaops/panda/pkg/operations"
	"github.com/ethpandaops/panda/pkg/serverapi"
)

var serverHTTP = &http.Client{Timeout: 0}

type rawServerResponse struct {
	Body        []byte
	ContentType string
}

func serverBaseURL() (string, error) {
	cfg, err := config.LoadClient(cfgFile)
	if err != nil {
		return "", fmt.Errorf("loading config: %w", err)
	}

	return cfg.ServerURL(), nil
}

func serverDo(
	ctx context.Context,
	method, path string,
	body io.Reader,
	query url.Values,
	headers map[string]string,
) ([]byte, int, http.Header, error) {
	baseURL, err := serverBaseURL()
	if err != nil {
		return nil, 0, nil, err
	}

	reqURL := strings.TrimRight(baseURL, "/") + path
	if len(query) > 0 {
		reqURL += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, body)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("creating request: %w", err)
	}

	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := serverHTTP.Do(req)
	if err != nil {
		if isConnectionRefused(err) {
			return nil, 0, nil, fmt.Errorf(
				"server is not running at %s — run 'panda init' or 'panda server start' first",
				baseURL,
			)
		}

		return nil, 0, nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, resp.Header.Clone(), fmt.Errorf("reading response: %w", err)
	}

	return data, resp.StatusCode, resp.Header.Clone(), nil
}

func serverGetJSON(ctx context.Context, path string, query url.Values, target any) error {
	data, status, _, err := serverDo(ctx, http.MethodGet, path, nil, query, nil)
	if err != nil {
		return err
	}

	if status < 200 || status >= 300 {
		return decodeAPIError(status, data)
	}

	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	return nil
}

func serverPostJSON(ctx context.Context, path string, payload any, target any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshaling request: %w", err)
	}

	data, status, _, err := serverDo(
		ctx,
		http.MethodPost,
		path,
		bytes.NewReader(body),
		nil,
		map[string]string{"Content-Type": "application/json"},
	)
	if err != nil {
		return err
	}

	if status < 200 || status >= 300 {
		return decodeAPIError(status, data)
	}

	if target == nil {
		return nil
	}

	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	return nil
}

func serverDelete(ctx context.Context, path string) error {
	data, status, _, err := serverDo(ctx, http.MethodDelete, path, nil, nil, nil)
	if err != nil {
		return err
	}

	if status < 200 || status >= 300 {
		return decodeAPIError(status, data)
	}

	return nil
}

func serverOperation(ctx context.Context, operationID string, args map[string]any) (*operations.Response, error) {
	var response operations.Response

	err := serverPostJSON(ctx, "/api/v1/operations/"+operationID, operations.Request{Args: args}, &response)
	if err != nil {
		return nil, err
	}

	return &response, nil
}

func serverOperationRaw(ctx context.Context, operationID string, args map[string]any) (*rawServerResponse, error) {
	body, err := json.Marshal(operations.Request{Args: args})
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	data, status, responseHeaders, err := serverDo(
		ctx,
		http.MethodPost,
		"/api/v1/operations/"+operationID,
		bytes.NewReader(body),
		nil,
		map[string]string{"Content-Type": "application/json"},
	)
	if err != nil {
		return nil, err
	}

	if status < 200 || status >= 300 {
		return nil, decodeAPIError(status, data)
	}

	return &rawServerResponse{
		Body:        data,
		ContentType: responseHeaders.Get("Content-Type"),
	}, nil
}

func runServerOperation(operationID string, args map[string]any) (*operations.Response, error) {
	return serverOperation(context.Background(), operationID, args)
}

func runServerOperationRaw(operationID string, args map[string]any) (*rawServerResponse, error) {
	return serverOperationRaw(context.Background(), operationID, args)
}

func listDatasources(ctx context.Context, filterType string) (*serverapi.DatasourcesResponse, error) {
	query := url.Values{}
	if filterType != "" {
		query.Set("type", filterType)
	}

	var response serverapi.DatasourcesResponse
	if err := serverGetJSON(ctx, "/api/v1/datasources", query, &response); err != nil {
		return nil, err
	}

	return &response, nil
}

func proxyAuthMetadata(ctx context.Context) (*serverapi.ProxyAuthMetadataResponse, error) {
	var response serverapi.ProxyAuthMetadataResponse
	if err := serverGetJSON(ctx, "/api/v1/proxy/auth", nil, &response); err != nil {
		return nil, err
	}

	return &response, nil
}

func executeCodeRemotely(ctx context.Context, req serverapi.ExecuteRequest) (*serverapi.ExecuteResponse, error) {
	var response serverapi.ExecuteResponse
	if err := serverPostJSON(ctx, "/api/v1/execute", req, &response); err != nil {
		return nil, err
	}

	return &response, nil
}

func listSessions(ctx context.Context) (*serverapi.ListSessionsResponse, error) {
	var response serverapi.ListSessionsResponse
	if err := serverGetJSON(ctx, "/api/v1/sessions", nil, &response); err != nil {
		return nil, err
	}

	return &response, nil
}

func createSession(ctx context.Context) (*serverapi.CreateSessionResponse, error) {
	var response serverapi.CreateSessionResponse
	if err := serverPostJSON(ctx, "/api/v1/sessions", map[string]any{}, &response); err != nil {
		return nil, err
	}

	return &response, nil
}

func destroySession(ctx context.Context, sessionID string) error {
	return serverDelete(ctx, "/api/v1/sessions/"+url.PathEscape(sessionID))
}

func searchExamples(ctx context.Context, queryText, category string, limit int) (*serverapi.SearchExamplesResponse, error) {
	query := url.Values{"query": []string{queryText}}
	if category != "" {
		query.Set("category", category)
	}
	if limit > 0 {
		query.Set("limit", fmt.Sprintf("%d", limit))
	}

	var response serverapi.SearchExamplesResponse
	if err := serverGetJSON(ctx, "/api/v1/search/examples", query, &response); err != nil {
		return nil, err
	}

	return &response, nil
}

func searchRunbooks(ctx context.Context, queryText, tag string, limit int) (*serverapi.SearchRunbooksResponse, error) {
	query := url.Values{"query": []string{queryText}}
	if tag != "" {
		query.Set("tag", tag)
	}
	if limit > 0 {
		query.Set("limit", fmt.Sprintf("%d", limit))
	}

	var response serverapi.SearchRunbooksResponse
	if err := serverGetJSON(ctx, "/api/v1/search/runbooks", query, &response); err != nil {
		return nil, err
	}

	return &response, nil
}

func searchEIPs(
	ctx context.Context,
	queryText, status, category, eipType string,
	limit int,
) (*serverapi.SearchEIPsResponse, error) {
	query := url.Values{"query": []string{queryText}}

	if status != "" {
		query.Set("status", status)
	}

	if category != "" {
		query.Set("category", category)
	}

	if eipType != "" {
		query.Set("type", eipType)
	}

	if limit > 0 {
		query.Set("limit", fmt.Sprintf("%d", limit))
	}

	var response serverapi.SearchEIPsResponse
	if err := serverGetJSON(ctx, "/api/v1/search/eips", query, &response); err != nil {
		return nil, err
	}

	return &response, nil
}

func listResources(ctx context.Context) (*serverapi.ListResourcesResponse, error) {
	var response serverapi.ListResourcesResponse
	if err := serverGetJSON(ctx, "/api/v1/resources", nil, &response); err != nil {
		return nil, err
	}

	return &response, nil
}

func readResource(ctx context.Context, uri string) (*serverapi.ResourceResponse, error) {
	return readResourceWithClientContext(ctx, uri, "")
}

func readResourceWithClientContext(ctx context.Context, uri, clientContext string) (*serverapi.ResourceResponse, error) {
	query := url.Values{"uri": []string{uri}}
	if clientContext != "" {
		query.Set("client_context", clientContext)
	}

	data, status, headers, err := serverDo(ctx, http.MethodGet, "/api/v1/resources/read", nil, query, nil)
	if err != nil {
		return nil, err
	}

	if status < 200 || status >= 300 {
		return nil, decodeAPIError(status, data)
	}

	return &serverapi.ResourceResponse{
		URI:      uri,
		MIMEType: headers.Get("Content-Type"),
		Content:  string(data),
	}, nil
}

func readClickHouseTables(ctx context.Context) (*clickhousemodule.TablesListResponse, error) {
	response, err := readResource(ctx, "clickhouse://tables")
	if err != nil {
		return nil, err
	}

	var payload clickhousemodule.TablesListResponse
	if err := json.Unmarshal([]byte(response.Content), &payload); err != nil {
		return nil, fmt.Errorf("decoding tables list: %w", err)
	}

	return &payload, nil
}

func readClickHouseTable(ctx context.Context, tableName string) (*clickhousemodule.TableDetailResponse, error) {
	response, err := readResource(ctx, "clickhouse://tables/"+tableName)
	if err != nil {
		return nil, err
	}

	var payload clickhousemodule.TableDetailResponse
	if err := json.Unmarshal([]byte(response.Content), &payload); err != nil {
		return nil, fmt.Errorf("decoding table detail: %w", err)
	}

	return &payload, nil
}

func decodeAPIError(status int, data []byte) error {
	var message string

	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err == nil {
		if msg, ok := payload["error"].(string); ok && msg != "" {
			message = msg
		}
	}

	if message == "" {
		message = strings.TrimSpace(string(data))
	}

	hint := serverErrorHint(status, message)
	if hint != "" {
		return fmt.Errorf("HTTP %d: %s\n\n  hint: %s", status, message, hint)
	}

	return fmt.Errorf("HTTP %d: %s", status, message)
}

func serverErrorHint(status int, _ string) string {
	switch status {
	case http.StatusNotFound:
		return "the panda server does not appear to be running at this address — start it with 'panda server start'"
	case http.StatusServiceUnavailable:
		return "the server is running but a required service (e.g. sandbox) is not available — check server logs with 'docker compose logs server'"
	default:
		return ""
	}
}

func isConnectionRefused(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}

	// Fallback: some wrapped errors don't propagate the syscall errno.
	return strings.Contains(err.Error(), "connection refused")
}
