package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/ethpandaops/panda/pkg/operations"
)

var ethnodeSegmentPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$|^[a-z0-9]$`)

func (s *service) registerEthNodeOperations() {
	s.registerOperation("ethnode.beacon_get", s.handleEthNodeBeaconGet)
	s.registerOperation("ethnode.beacon_post", s.handleEthNodeBeaconPost)
	s.registerOperation("ethnode.execution_rpc", s.handleEthNodeExecutionRPC)
	s.registerOperation("ethnode.get_node_version", func(w http.ResponseWriter, r *http.Request) {
		s.handleEthNodeCuratedBeaconGet(w, r, "/eth/v1/node/version")
	})
	s.registerOperation("ethnode.get_node_syncing", func(w http.ResponseWriter, r *http.Request) {
		s.handleEthNodeCuratedBeaconGet(w, r, "/eth/v1/node/syncing")
	})
	s.registerOperation("ethnode.get_peers", func(w http.ResponseWriter, r *http.Request) {
		s.handleEthNodeCuratedBeaconGet(w, r, "/eth/v1/node/peers")
	})
	s.registerOperation("ethnode.get_peer_count", func(w http.ResponseWriter, r *http.Request) {
		s.handleEthNodeCuratedBeaconGet(w, r, "/eth/v1/node/peer_count")
	})
	s.registerOperation("ethnode.get_beacon_headers", s.handleEthNodeBeaconHeaders)
	s.registerOperation("ethnode.get_finality_checkpoints", s.handleEthNodeFinalityCheckpoints)
	s.registerOperation("ethnode.get_config_spec", func(w http.ResponseWriter, r *http.Request) {
		s.handleEthNodeCuratedBeaconGet(w, r, "/eth/v1/config/spec")
	})
	s.registerOperation("ethnode.get_fork_schedule", func(w http.ResponseWriter, r *http.Request) {
		s.handleEthNodeCuratedBeaconGet(w, r, "/eth/v1/config/fork_schedule")
	})
	s.registerOperation("ethnode.get_deposit_contract", func(w http.ResponseWriter, r *http.Request) {
		s.handleEthNodeCuratedBeaconGet(w, r, "/eth/v1/config/deposit_contract")
	})
	s.registerOperation("ethnode.get_node_health", s.handleEthNodeHealth)
	s.registerOperation("ethnode.eth_block_number", func(w http.ResponseWriter, r *http.Request) {
		s.handleEthNodeHexRPC(w, r, "eth_blockNumber", "block_number")
	})
	s.registerOperation("ethnode.eth_syncing", func(w http.ResponseWriter, r *http.Request) {
		s.handleEthNodeExecutionRPCMethod(w, r, "eth_syncing")
	})
	s.registerOperation("ethnode.eth_chain_id", func(w http.ResponseWriter, r *http.Request) {
		s.handleEthNodeHexRPC(w, r, "eth_chainId", "chain_id")
	})
	s.registerOperation("ethnode.eth_get_block_by_number", s.handleEthNodeGetBlockByNumber)
	s.registerOperation("ethnode.net_peer_count", func(w http.ResponseWriter, r *http.Request) {
		s.handleEthNodeHexRPC(w, r, "net_peerCount", "peer_count")
	})
	s.registerOperation("ethnode.web3_client_version", func(w http.ResponseWriter, r *http.Request) {
		s.handleEthNodeExecutionRPCMethod(w, r, "web3_clientVersion")
	})
}

func (s *service) handleEthNodeBeaconGet(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	network, instance, path, err := parseEthNodeBeaconArgs(req.Args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	body, contentType, status, err := s.ethNodeBeaconRequestRaw(
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

func (s *service) handleEthNodeBeaconPost(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	network, instance, path, err := parseEthNodeBeaconArgs(req.Args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	body, contentType, status, err := s.ethNodeBeaconRequestRaw(
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

func (s *service) handleEthNodeExecutionRPC(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	network, instance, err := parseEthNodeNodeArgs(req.Args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	method, err := requiredStringArg(req.Args, "method")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	body, contentType, status, err := s.ethNodeExecutionRPCRaw(r.Context(), network, instance, method, optionalSliceArg(req.Args, "params"))
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	writePassthroughResponse(w, http.StatusOK, contentType, body)
}

func (s *service) handleEthNodeCuratedBeaconGet(w http.ResponseWriter, r *http.Request, path string) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	network, instance, err := parseEthNodeNodeArgs(req.Args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	data, status, err := s.ethNodeBeaconRequest(r.Context(), http.MethodGet, network, instance, path, nil, nil)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: data,
		Meta: map[string]any{
			"network":  network,
			"instance": instance,
			"path":     path,
		},
	})
}

func (s *service) handleEthNodeBeaconHeaders(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	slot := optionalStringArg(req.Args, "slot")
	if slot == "" {
		slot = "head"
	}

	s.handleEthNodeCuratedBeaconPath(w, r, req, fmt.Sprintf("/eth/v1/beacon/headers/%s", slot))
}

func (s *service) handleEthNodeFinalityCheckpoints(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	stateID := optionalStringArg(req.Args, "state_id")
	if stateID == "" {
		stateID = "head"
	}

	s.handleEthNodeCuratedBeaconPath(w, r, req, fmt.Sprintf("/eth/v1/beacon/states/%s/finality_checkpoints", stateID))
}

func (s *service) handleEthNodeCuratedBeaconPath(w http.ResponseWriter, r *http.Request, req operations.Request, path string) {
	network, instance, err := parseEthNodeNodeArgs(req.Args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	data, status, err := s.ethNodeBeaconRequest(r.Context(), http.MethodGet, network, instance, path, nil, nil)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: data,
		Meta: map[string]any{
			"network":  network,
			"instance": instance,
			"path":     path,
		},
	})
}

func (s *service) handleEthNodeHealth(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	network, instance, err := parseEthNodeNodeArgs(req.Args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	statusCode, status, err := s.ethNodeBeaconHealthRequest(r.Context(), network, instance)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: map[string]any{"status_code": statusCode},
		Meta: map[string]any{
			"network":  network,
			"instance": instance,
		},
	})
}

func (s *service) handleEthNodeHexRPC(w http.ResponseWriter, r *http.Request, method, field string) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	network, instance, err := parseEthNodeNodeArgs(req.Args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	result, status, err := s.ethNodeExecutionRPC(r.Context(), network, instance, method, nil)
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

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
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

func (s *service) handleEthNodeExecutionRPCMethod(w http.ResponseWriter, r *http.Request, method string) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	network, instance, err := parseEthNodeNodeArgs(req.Args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	result, status, err := s.ethNodeExecutionRPC(r.Context(), network, instance, method, nil)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: result,
		Meta: map[string]any{
			"network":  network,
			"instance": instance,
			"method":   method,
		},
	})
}

func (s *service) handleEthNodeGetBlockByNumber(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	network, instance, err := parseEthNodeNodeArgs(req.Args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	block := optionalStringArg(req.Args, "block")
	if block == "" {
		block = "latest"
	}

	fullTx, _ := req.Args["full_tx"].(bool)

	result, status, err := s.ethNodeExecutionRPC(r.Context(), network, instance, "eth_getBlockByNumber", []any{block, fullTx})
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: result,
		Meta: map[string]any{
			"network":  network,
			"instance": instance,
			"method":   "eth_getBlockByNumber",
		},
	})
}

func parseEthNodeBeaconArgs(args map[string]any) (string, string, string, error) {
	network, instance, err := parseEthNodeNodeArgs(args)
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

func parseEthNodeNodeArgs(args map[string]any) (string, string, error) {
	network, err := requiredStringArg(args, "network")
	if err != nil {
		return "", "", err
	}

	instance, err := requiredStringArg(args, "instance")
	if err != nil {
		return "", "", err
	}

	if !ethnodeSegmentPattern.MatchString(network) {
		return "", "", fmt.Errorf("invalid network name: must match [a-z0-9-]")
	}

	if !ethnodeSegmentPattern.MatchString(instance) {
		return "", "", fmt.Errorf("invalid instance name: must match [a-z0-9-]")
	}

	return network, instance, nil
}

func (s *service) ethNodeBeaconRequest(
	ctx context.Context,
	method, network, instance, path string,
	params map[string]any,
	body any,
) (any, int, error) {
	responseBody, _, status, err := s.ethNodeBeaconRequestRaw(ctx, method, network, instance, path, params, body)
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

func (s *service) ethNodeBeaconRequestRaw(
	ctx context.Context,
	method, network, instance, path string,
	params map[string]any,
	body any,
) ([]byte, string, int, error) {
	values := url.Values{}
	for key, value := range params {
		values.Set(key, fmt.Sprint(value))
	}

	requestPath := "/beacon/" + network + "/" + instance + path
	if len(values) > 0 {
		requestPath += "?" + values.Encode()
	}

	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, "", http.StatusBadRequest, fmt.Errorf("marshaling request body: %w", err)
		}
		reader = bytes.NewReader(payload)
	}

	headers := http.Header{}
	if body != nil {
		headers.Set("Content-Type", "application/json")
	}

	data, status, responseHeaders, err := s.proxyRequest(ctx, method, requestPath, reader, headers)
	if err != nil {
		return nil, "", http.StatusBadGateway, err
	}

	if status < 200 || status >= 300 {
		return nil, "", status, fmt.Errorf(
			"%s",
			upstreamFailureMessage(
				"ethnode.beacon",
				status,
				data,
				"network="+network,
				"instance="+instance,
				"path="+path,
			),
		)
	}

	contentType := responseHeaders.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}

	return data, contentType, http.StatusOK, nil
}

func (s *service) ethNodeBeaconHealthRequest(ctx context.Context, network, instance string) (int, int, error) {
	_, status, _, err := s.proxyRequest(ctx, http.MethodGet, "/beacon/"+network+"/"+instance+"/eth/v1/node/health", nil, nil)
	if err != nil {
		return 0, http.StatusBadGateway, err
	}

	return status, http.StatusOK, nil
}

func (s *service) ethNodeExecutionRPC(
	ctx context.Context,
	network, instance, method string,
	params []any,
) (any, int, error) {
	body, _, status, err := s.ethNodeExecutionRPCRaw(ctx, network, instance, method, params)
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

func (s *service) ethNodeExecutionRPCRaw(
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

	data, status, responseHeaders, err := s.proxyRequest(
		ctx,
		http.MethodPost,
		"/execution/"+network+"/"+instance+"/",
		bytes.NewReader(payload),
		http.Header{"Content-Type": []string{"application/json"}},
	)
	if err != nil {
		return nil, "", http.StatusBadGateway, err
	}

	if status < 200 || status >= 300 {
		return nil, "", status, fmt.Errorf(
			"%s",
			upstreamFailureMessage(
				"ethnode.execution_rpc",
				status,
				data,
				"network="+network,
				"instance="+instance,
				"method="+method,
			),
		)
	}

	var rpcResp struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &rpcResp); err != nil {
		return nil, "", http.StatusBadGateway, fmt.Errorf("invalid JSON-RPC response: %w", err)
	}

	if rpcResp.Error != nil {
		return nil, "", http.StatusBadGateway, fmt.Errorf("JSON-RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	contentType := responseHeaders.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}

	return data, contentType, http.StatusOK, nil
}
