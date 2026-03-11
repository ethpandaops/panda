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

func serverOperationJSON[Args any, Data any](ctx context.Context, operationID string, args Args) (Data, error) {
	var zero Data
	var response operations.Response

	err := serverPostJSON(ctx, "/api/v1/operations/"+operationID, operations.TypedRequest[Args]{Args: args}, &response)
	if err != nil {
		return zero, err
	}

	return operations.DecodeResponseData[Data](&response)
}

func serverOperationRaw[Args any](ctx context.Context, operationID string, args Args) (*rawServerResponse, error) {
	body, err := json.Marshal(operations.TypedRequest[Args]{Args: args})
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

func listClickHouseDatasources() ([]operations.Datasource, error) {
	response, err := serverOperationJSON[operations.NoArgs, operations.DatasourcesPayload](
		context.Background(),
		"clickhouse.list_datasources",
		operations.NoArgs{},
	)
	if err != nil {
		return nil, err
	}

	return response.Datasources, nil
}

func clickHouseQuery(ctx context.Context, args operations.ClickHouseQueryArgs) (*rawServerResponse, error) {
	return serverOperationRaw(ctx, "clickhouse.query", args)
}

func clickHouseQueryRaw(ctx context.Context, args operations.ClickHouseQueryArgs) (*rawServerResponse, error) {
	return serverOperationRaw(ctx, "clickhouse.query_raw", args)
}

func listPrometheusDatasources() ([]operations.Datasource, error) {
	response, err := serverOperationJSON[operations.NoArgs, operations.DatasourcesPayload](
		context.Background(),
		"prometheus.list_datasources",
		operations.NoArgs{},
	)
	if err != nil {
		return nil, err
	}

	return response.Datasources, nil
}

func prometheusQuery(args operations.PrometheusQueryArgs) (*rawServerResponse, error) {
	return serverOperationRaw(context.Background(), "prometheus.query", args)
}

func prometheusQueryRange(args operations.PrometheusRangeQueryArgs) (*rawServerResponse, error) {
	return serverOperationRaw(context.Background(), "prometheus.query_range", args)
}

func prometheusLabels(args operations.DatasourceArgs) (*rawServerResponse, error) {
	return serverOperationRaw(context.Background(), "prometheus.get_labels", args)
}

func prometheusLabelValues(args operations.DatasourceLabelArgs) (*rawServerResponse, error) {
	return serverOperationRaw(context.Background(), "prometheus.get_label_values", args)
}

func listLokiDatasources() ([]operations.Datasource, error) {
	response, err := serverOperationJSON[operations.NoArgs, operations.DatasourcesPayload](
		context.Background(),
		"loki.list_datasources",
		operations.NoArgs{},
	)
	if err != nil {
		return nil, err
	}

	return response.Datasources, nil
}

func lokiQuery(args operations.LokiQueryArgs) (*rawServerResponse, error) {
	return serverOperationRaw(context.Background(), "loki.query", args)
}

func lokiInstantQuery(args operations.LokiInstantQueryArgs) (*rawServerResponse, error) {
	return serverOperationRaw(context.Background(), "loki.query_instant", args)
}

func lokiLabels(args operations.LokiLabelsArgs) (*rawServerResponse, error) {
	return serverOperationRaw(context.Background(), "loki.get_labels", args)
}

func lokiLabelValues(args operations.LokiLabelValuesArgs) (*rawServerResponse, error) {
	return serverOperationRaw(context.Background(), "loki.get_label_values", args)
}

func listDoraNetworks() ([]operations.DoraNetwork, error) {
	response, err := serverOperationJSON[operations.NoArgs, operations.DoraNetworksPayload](
		context.Background(),
		"dora.list_networks",
		operations.NoArgs{},
	)
	if err != nil {
		return nil, err
	}

	return response.Networks, nil
}

func doraOverview(args operations.DoraNetworkArgs) (operations.DoraOverviewPayload, error) {
	return serverOperationJSON[operations.DoraNetworkArgs, operations.DoraOverviewPayload](
		context.Background(),
		"dora.get_network_overview",
		args,
	)
}

func doraValidator(args operations.DoraIndexOrPubkeyArgs) (*rawServerResponse, error) {
	return serverOperationRaw(context.Background(), "dora.get_validator", args)
}

func doraSlot(args operations.DoraSlotOrHashArgs) (*rawServerResponse, error) {
	return serverOperationRaw(context.Background(), "dora.get_slot", args)
}

func doraEpoch(args operations.DoraEpochArgs) (*rawServerResponse, error) {
	return serverOperationRaw(context.Background(), "dora.get_epoch", args)
}

func ethNodeSyncing(args operations.EthNodeNodeArgs) (operations.EthNodeSyncingPayload, error) {
	return serverOperationJSON[operations.EthNodeNodeArgs, operations.EthNodeSyncingPayload](
		context.Background(),
		"ethnode.get_node_syncing",
		args,
	)
}

func ethNodeVersion(args operations.EthNodeNodeArgs) (operations.EthNodeVersionPayload, error) {
	return serverOperationJSON[operations.EthNodeNodeArgs, operations.EthNodeVersionPayload](
		context.Background(),
		"ethnode.get_node_version",
		args,
	)
}

func ethNodeExecutionClientVersion(args operations.EthNodeNodeArgs) (string, error) {
	return serverOperationJSON[operations.EthNodeNodeArgs, string](
		context.Background(),
		"ethnode.web3_client_version",
		args,
	)
}

func ethNodeHealth(args operations.EthNodeNodeArgs) (operations.StatusCodePayload, error) {
	return serverOperationJSON[operations.EthNodeNodeArgs, operations.StatusCodePayload](
		context.Background(),
		"ethnode.get_node_health",
		args,
	)
}

func ethNodePeerCount(args operations.EthNodeNodeArgs) (operations.EthNodePeerCountPayload, error) {
	return serverOperationJSON[operations.EthNodeNodeArgs, operations.EthNodePeerCountPayload](
		context.Background(),
		"ethnode.get_peer_count",
		args,
	)
}

func ethNodeFinality(args operations.EthNodeFinalityArgs) (operations.EthNodeFinalityPayload, error) {
	return serverOperationJSON[operations.EthNodeFinalityArgs, operations.EthNodeFinalityPayload](
		context.Background(),
		"ethnode.get_finality_checkpoints",
		args,
	)
}

func ethNodeHeaders(args operations.EthNodeBeaconHeadersArgs) (operations.EthNodeHeaderPayload, error) {
	return serverOperationJSON[operations.EthNodeBeaconHeadersArgs, operations.EthNodeHeaderPayload](
		context.Background(),
		"ethnode.get_beacon_headers",
		args,
	)
}

func ethNodeBlockNumber(args operations.EthNodeNodeArgs) (operations.EthNodeBlockNumberPayload, error) {
	return serverOperationJSON[operations.EthNodeNodeArgs, operations.EthNodeBlockNumberPayload](
		context.Background(),
		"ethnode.eth_block_number",
		args,
	)
}

func ethNodeBeaconGet(args operations.EthNodeBeaconGetArgs) (*rawServerResponse, error) {
	return serverOperationRaw(context.Background(), "ethnode.beacon_get", args)
}

func ethNodeExecutionRPC(args operations.EthNodeExecutionRPCArgs) (*rawServerResponse, error) {
	return serverOperationRaw(context.Background(), "ethnode.execution_rpc", args)
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
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err == nil {
		if message, ok := payload["error"].(string); ok && message != "" {
			return fmt.Errorf("HTTP %d: %s", status, message)
		}
	}

	return fmt.Errorf("HTTP %d: %s", status, strings.TrimSpace(string(data)))
}

func isConnectionRefused(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}

	// Fallback: some wrapped errors don't propagate the syscall errno.
	return strings.Contains(err.Error(), "connection refused")
}
