package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/mcp/pkg/operations"
)

type EthNodeOperationsHandler struct {
	log    logrus.FieldLogger
	cfg    EthNodeConfig
	client *http.Client
}

func NewEthNodeOperationsHandler(log logrus.FieldLogger, cfg EthNodeConfig) *EthNodeOperationsHandler {
	return &EthNodeOperationsHandler{
		log:    log.WithField("handler", "ethnode-operations"),
		cfg:    cfg,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (h *EthNodeOperationsHandler) HandleOperation(operationID string, w http.ResponseWriter, r *http.Request) bool {
	switch operationID {
	case "ethnode.beacon_get":
		h.handleBeaconGet(w, r)
	case "ethnode.beacon_post":
		h.handleBeaconPost(w, r)
	case "ethnode.execution_rpc":
		h.handleExecutionRPC(w, r)
	case "ethnode.get_node_version":
		h.handleCuratedBeaconGet(w, r, "/eth/v1/node/version")
	case "ethnode.get_node_syncing":
		h.handleCuratedBeaconGet(w, r, "/eth/v1/node/syncing")
	case "ethnode.get_peers":
		h.handleCuratedBeaconGet(w, r, "/eth/v1/node/peers")
	case "ethnode.get_peer_count":
		h.handleCuratedBeaconGet(w, r, "/eth/v1/node/peer_count")
	case "ethnode.get_beacon_headers":
		h.handleBeaconHeaders(w, r)
	case "ethnode.get_finality_checkpoints":
		h.handleFinalityCheckpoints(w, r)
	case "ethnode.get_config_spec":
		h.handleCuratedBeaconGet(w, r, "/eth/v1/config/spec")
	case "ethnode.get_fork_schedule":
		h.handleCuratedBeaconGet(w, r, "/eth/v1/config/fork_schedule")
	case "ethnode.get_deposit_contract":
		h.handleCuratedBeaconGet(w, r, "/eth/v1/config/deposit_contract")
	case "ethnode.get_node_health":
		h.handleHealth(w, r)
	case "ethnode.eth_block_number":
		h.handleHexRPC(w, r, "eth_blockNumber", "block_number")
	case "ethnode.eth_syncing":
		h.handleExecutionRPCMethod(w, r, "eth_syncing")
	case "ethnode.eth_chain_id":
		h.handleHexRPC(w, r, "eth_chainId", "chain_id")
	case "ethnode.eth_get_block_by_number":
		h.handleGetBlockByNumber(w, r)
	case "ethnode.net_peer_count":
		h.handleHexRPC(w, r, "net_peerCount", "peer_count")
	case "ethnode.web3_client_version":
		h.handleExecutionRPCMethod(w, r, "web3_clientVersion")
	default:
		return false
	}

	return true
}

func (h *EthNodeOperationsHandler) handleBeaconGet(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	network, instance, path, err := h.parseBeaconArgs(req.Args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	body, contentType, status, err := h.doBeaconRequestRaw(
		r.Context(),
		http.MethodGet,
		network,
		instance,
		path,
		optionalMapArg(req.Args, "params"),
		nil,
	)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	writePassthroughResponse(w, http.StatusOK, contentType, body)
}

func (h *EthNodeOperationsHandler) handleBeaconPost(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	network, instance, path, err := h.parseBeaconArgs(req.Args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	body, contentType, status, err := h.doBeaconRequestRaw(
		r.Context(),
		http.MethodPost,
		network,
		instance,
		path,
		nil,
		req.Args["body"],
	)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	writePassthroughResponse(w, http.StatusOK, contentType, body)
}

func (h *EthNodeOperationsHandler) handleExecutionRPC(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	network, instance, err := h.parseNodeArgs(req.Args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	method, err := requiredStringArg(req.Args, "method")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	body, contentType, status, err := h.doExecutionRPCRaw(
		r.Context(),
		network,
		instance,
		method,
		optionalSliceArg(req.Args, "params"),
	)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	writePassthroughResponse(w, http.StatusOK, contentType, body)
}

func (h *EthNodeOperationsHandler) handleCuratedBeaconGet(w http.ResponseWriter, r *http.Request, path string) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	network, instance, err := h.parseNodeArgs(req.Args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	data, status, err := h.doBeaconRequest(r.Context(), http.MethodGet, network, instance, path, nil, nil)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	writeOperationResponse(h.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: data,
		Meta: map[string]any{
			"network":  network,
			"instance": instance,
			"path":     path,
		},
	})
}

func (h *EthNodeOperationsHandler) handleBeaconHeaders(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	slot := optionalStringArg(req.Args, "slot")
	if slot == "" {
		slot = "head"
	}

	h.handleCuratedBeaconPath(w, r, req, fmt.Sprintf("/eth/v1/beacon/headers/%s", slot))
}

func (h *EthNodeOperationsHandler) handleFinalityCheckpoints(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	stateID := optionalStringArg(req.Args, "state_id")
	if stateID == "" {
		stateID = "head"
	}

	h.handleCuratedBeaconPath(w, r, req, fmt.Sprintf("/eth/v1/beacon/states/%s/finality_checkpoints", stateID))
}

func (h *EthNodeOperationsHandler) handleCuratedBeaconPath(
	w http.ResponseWriter,
	r *http.Request,
	req operations.Request,
	path string,
) {
	network, instance, err := h.parseNodeArgs(req.Args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	data, status, err := h.doBeaconRequest(r.Context(), http.MethodGet, network, instance, path, nil, nil)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	writeOperationResponse(h.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: data,
		Meta: map[string]any{
			"network":  network,
			"instance": instance,
			"path":     path,
		},
	})
}

func (h *EthNodeOperationsHandler) handleHealth(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	network, instance, err := h.parseNodeArgs(req.Args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	statusCode, status, err := h.doBeaconHealthRequest(r.Context(), network, instance)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	writeOperationResponse(h.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: map[string]any{"status_code": statusCode},
		Meta: map[string]any{
			"network":  network,
			"instance": instance,
		},
	})
}

func (h *EthNodeOperationsHandler) handleHexRPC(w http.ResponseWriter, r *http.Request, method, field string) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	network, instance, err := h.parseNodeArgs(req.Args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	result, status, err := h.doExecutionRPC(r.Context(), network, instance, method, nil)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	hexValue, ok := result.(string)
	if !ok {
		http.Error(w, "unexpected JSON-RPC result shape", http.StatusBadGateway)
		return
	}

	parsedValue, err := parseHexUint64(hexValue)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	writeOperationResponse(h.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: map[string]any{
			"hex": hexValue,
			field: parsedValue,
		},
		Meta: map[string]any{
			"network":  network,
			"instance": instance,
			"method":   method,
		},
	})
}

func (h *EthNodeOperationsHandler) handleExecutionRPCMethod(w http.ResponseWriter, r *http.Request, method string) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	network, instance, err := h.parseNodeArgs(req.Args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	result, status, err := h.doExecutionRPC(r.Context(), network, instance, method, nil)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	writeOperationResponse(h.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: result,
		Meta: map[string]any{
			"network":  network,
			"instance": instance,
			"method":   method,
		},
	})
}

func (h *EthNodeOperationsHandler) handleGetBlockByNumber(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	network, instance, err := h.parseNodeArgs(req.Args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	block := optionalStringArg(req.Args, "block")
	if block == "" {
		block = "latest"
	}

	fullTx, _ := req.Args["full_tx"].(bool)

	result, status, err := h.doExecutionRPC(r.Context(), network, instance, "eth_getBlockByNumber", []any{block, fullTx})
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	writeOperationResponse(h.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: result,
		Meta: map[string]any{
			"network":  network,
			"instance": instance,
			"method":   "eth_getBlockByNumber",
		},
	})
}

func (h *EthNodeOperationsHandler) parseBeaconArgs(args map[string]any) (string, string, string, error) {
	network, instance, err := h.parseNodeArgs(args)
	if err != nil {
		return "", "", "", err
	}

	path, err := requiredStringArg(args, "path")
	if err != nil {
		return "", "", "", err
	}

	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	return network, instance, path, nil
}

func (h *EthNodeOperationsHandler) parseNodeArgs(args map[string]any) (string, string, error) {
	network, err := requiredStringArg(args, "network")
	if err != nil {
		return "", "", err
	}

	instance, err := requiredStringArg(args, "instance")
	if err != nil {
		return "", "", err
	}

	if !validSegment.MatchString(network) {
		return "", "", fmt.Errorf("invalid network name: must match [a-z0-9-]")
	}

	if !validSegment.MatchString(instance) {
		return "", "", fmt.Errorf("invalid instance name: must match [a-z0-9-]")
	}

	return network, instance, nil
}

func (h *EthNodeOperationsHandler) doBeaconRequest(
	ctx context.Context,
	method, network, instance, path string,
	params map[string]any,
	body any,
) (any, int, error) {
	responseBody, _, status, err := h.doBeaconRequestRaw(ctx, method, network, instance, path, params, body)
	if err != nil {
		return nil, status, err
	}

	if len(responseBody) == 0 {
		return map[string]any{}, http.StatusOK, nil
	}

	var data any
	if err := json.Unmarshal(responseBody, &data); err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("invalid beacon JSON response: %w", err)
	}

	return data, http.StatusOK, nil
}

func (h *EthNodeOperationsHandler) doBeaconRequestRaw(
	ctx context.Context,
	method, network, instance, path string,
	params map[string]any,
	body any,
) ([]byte, string, int, error) {
	requestURL := fmt.Sprintf("https://bn-%s.srv.%s.ethpandaops.io%s", instance, network, path)
	if len(params) > 0 {
		values := url.Values{}
		for key, value := range params {
			values.Set(key, fmt.Sprint(value))
		}
		requestURL += "?" + values.Encode()
	}

	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, "", http.StatusBadRequest, fmt.Errorf("marshaling request body: %w", err)
		}
		reader = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, requestURL, reader)
	if err != nil {
		return nil, "", http.StatusInternalServerError, fmt.Errorf("creating beacon request: %w", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if h.cfg.Username != "" {
		req.SetBasicAuth(h.cfg.Username, h.cfg.Password)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, "", http.StatusBadGateway, fmt.Errorf("executing beacon request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", http.StatusBadGateway, fmt.Errorf("reading beacon response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", resp.StatusCode, fmt.Errorf("%s", strings.TrimSpace(string(responseBody)))
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}

	return responseBody, contentType, http.StatusOK, nil
}

func (h *EthNodeOperationsHandler) doBeaconHealthRequest(ctx context.Context, network, instance string) (int, int, error) {
	requestURL := fmt.Sprintf("https://bn-%s.srv.%s.ethpandaops.io/eth/v1/node/health", instance, network)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return 0, http.StatusInternalServerError, fmt.Errorf("creating beacon health request: %w", err)
	}

	if h.cfg.Username != "" {
		req.SetBasicAuth(h.cfg.Username, h.cfg.Password)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return 0, http.StatusBadGateway, fmt.Errorf("executing beacon health request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	return resp.StatusCode, http.StatusOK, nil
}

func (h *EthNodeOperationsHandler) doExecutionRPC(
	ctx context.Context,
	network, instance, method string,
	params []any,
) (any, int, error) {
	body, _, status, err := h.doExecutionRPCRaw(ctx, network, instance, method, params)
	if err != nil {
		return nil, status, err
	}

	var rpcResp struct {
		Result any `json:"result"`
	}
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("invalid JSON-RPC response: %w", err)
	}

	return rpcResp.Result, http.StatusOK, nil
}

func (h *EthNodeOperationsHandler) doExecutionRPCRaw(
	ctx context.Context,
	network, instance, method string,
	params []any,
) ([]byte, string, int, error) {
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
		"id":      1,
	})
	if err != nil {
		return nil, "", http.StatusBadRequest, fmt.Errorf("marshaling JSON-RPC request: %w", err)
	}

	requestURL := fmt.Sprintf("https://rpc-%s.srv.%s.ethpandaops.io/", instance, network)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(payload))
	if err != nil {
		return nil, "", http.StatusInternalServerError, fmt.Errorf("creating JSON-RPC request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if h.cfg.Username != "" {
		req.SetBasicAuth(h.cfg.Username, h.cfg.Password)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, "", http.StatusBadGateway, fmt.Errorf("executing JSON-RPC request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", http.StatusBadGateway, fmt.Errorf("reading JSON-RPC response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", resp.StatusCode, fmt.Errorf("%s", strings.TrimSpace(string(body)))
	}

	var rpcResp struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return nil, "", http.StatusBadGateway, fmt.Errorf("invalid JSON-RPC response: %w", err)
	}

	if rpcResp.Error != nil {
		return nil, "", http.StatusBadGateway, fmt.Errorf("JSON-RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}

	return body, contentType, http.StatusOK, nil
}

func parseHexUint64(value string) (uint64, error) {
	value = strings.TrimPrefix(value, "0x")
	if value == "" {
		return 0, nil
	}

	parsed, err := strconv.ParseUint(value, 16, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid hex value %q: %w", value, err)
	}

	return parsed, nil
}
