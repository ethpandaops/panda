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

type ethNodeOperationRequest struct {
	Args     map[string]any
	Network  string
	Instance string
}

func (s *service) registerEthNodeOperations() {
	s.registerOperation("ethnode.beacon_get", s.handleEthNodeBeaconGet)
	s.registerOperation("ethnode.beacon_post", s.handleEthNodeBeaconPost)
	s.registerOperation("ethnode.execution_rpc", s.handleEthNodeExecutionRPC)
	s.registerOperation("ethnode.get_node_version", func(w http.ResponseWriter, r *http.Request) {
		handleTypedEthNodeBeaconResponse[operations.EthNodeVersionPayload](s, w, r, "/eth/v1/node/version")
	})
	s.registerOperation("ethnode.get_node_syncing", func(w http.ResponseWriter, r *http.Request) {
		handleTypedEthNodeBeaconResponse[operations.EthNodeSyncingPayload](s, w, r, "/eth/v1/node/syncing")
	})
	s.registerOperation("ethnode.get_peers", func(w http.ResponseWriter, r *http.Request) {
		s.handleEthNodeCuratedBeaconGet(w, r, "/eth/v1/node/peers")
	})
	s.registerOperation("ethnode.get_peer_count", func(w http.ResponseWriter, r *http.Request) {
		handleTypedEthNodeBeaconResponse[operations.EthNodePeerCountPayload](s, w, r, "/eth/v1/node/peer_count")
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
		s.handleEthNodeExecutionRPCStringMethod(w, r, "web3_clientVersion")
	})
}

func (s *service) handleEthNodeBeaconGet(w http.ResponseWriter, r *http.Request) {
	s.withEthNodeRequest(w, r, func(req ethNodeOperationRequest) {
		path, err := parseEthNodePathArg(req.Args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		body, contentType, status, err := s.ethNodeBeaconRequestRaw(
			r.Context(),
			http.MethodGet,
			req.Network,
			req.Instance,
			path,
			optionalMapArg(req.Args, "params"),
			nil,
		)
		if err != nil {
			http.Error(w, err.Error(), status)
			return
		}

		writePassthroughResponse(w, http.StatusOK, contentType, body)
	})
}

func (s *service) handleEthNodeBeaconPost(w http.ResponseWriter, r *http.Request) {
	s.withEthNodeRequest(w, r, func(req ethNodeOperationRequest) {
		path, err := parseEthNodePathArg(req.Args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		body, contentType, status, err := s.ethNodeBeaconRequestRaw(
			r.Context(),
			http.MethodPost,
			req.Network,
			req.Instance,
			path,
			nil,
			req.Args["body"],
		)
		if err != nil {
			http.Error(w, err.Error(), status)
			return
		}

		writePassthroughResponse(w, http.StatusOK, contentType, body)
	})
}

func (s *service) handleEthNodeExecutionRPC(w http.ResponseWriter, r *http.Request) {
	s.withEthNodeRequest(w, r, func(req ethNodeOperationRequest) {
		method, err := requiredStringArg(req.Args, "method")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		body, contentType, status, err := s.ethNodeExecutionRPCRaw(
			r.Context(),
			req.Network,
			req.Instance,
			method,
			optionalSliceArg(req.Args, "params"),
		)
		if err != nil {
			http.Error(w, err.Error(), status)
			return
		}

		writePassthroughResponse(w, http.StatusOK, contentType, body)
	})
}

func (s *service) handleEthNodeCuratedBeaconGet(w http.ResponseWriter, r *http.Request, path string) {
	s.withEthNodeRequest(w, r, func(req ethNodeOperationRequest) {
		s.writeEthNodeCuratedBeaconResponse(w, r, req, path)
	})
}

func (s *service) handleEthNodeBeaconHeaders(w http.ResponseWriter, r *http.Request) {
	request, err := decodeTypedOperationArgs[operations.EthNodeBeaconHeadersArgs](r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	nodeRequest, err := validateEthNodeNodeRequest(request.Network, request.Instance)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	slot := strings.TrimSpace(request.Slot)
	if slot == "" {
		slot = "head"
	}

	writeTypedEthNodeBeaconResponse[operations.EthNodeHeaderPayload](
		s,
		w,
		r,
		nodeRequest,
		fmt.Sprintf("/eth/v1/beacon/headers/%s", slot),
	)
}

func (s *service) handleEthNodeFinalityCheckpoints(w http.ResponseWriter, r *http.Request) {
	request, err := decodeTypedOperationArgs[operations.EthNodeFinalityArgs](r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	nodeRequest, err := validateEthNodeNodeRequest(request.Network, request.Instance)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	stateID := strings.TrimSpace(request.StateID)
	if stateID == "" {
		stateID = "head"
	}

	writeTypedEthNodeBeaconResponse[operations.EthNodeFinalityPayload](
		s,
		w,
		r,
		nodeRequest,
		fmt.Sprintf("/eth/v1/beacon/states/%s/finality_checkpoints", stateID),
	)
}

func (s *service) handleEthNodeHealth(w http.ResponseWriter, r *http.Request) {
	request, err := decodeValidatedEthNodeNodeArgs(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	statusCode, status, err := s.ethNodeBeaconHealthRequest(r.Context(), request.Network, request.Instance)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	writeObjectOperationResponse(
		s.log,
		w,
		http.StatusOK,
		operations.StatusCodePayload{StatusCode: statusCode},
		ethNodeMeta(request, ""),
	)
}

func (s *service) handleEthNodeHexRPC(w http.ResponseWriter, r *http.Request, method, field string) {
	request, err := decodeValidatedEthNodeNodeArgs(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	result, status, err := s.ethNodeExecutionRPC(r.Context(), request.Network, request.Instance, method, nil)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	hexValue, err := operations.DecodeValue[string](result)
	if err != nil {
		http.Error(w, "unexpected JSON-RPC result shape", http.StatusBadGateway)
		return
	}

	parsedValue, err := parseHexUint64(hexValue)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	payload, err := ethNodeHexPayload(field, hexValue, parsedValue)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeObjectOperationResponse(s.log, w, http.StatusOK, payload, ethNodeMeta(request, method))
}

func (s *service) handleEthNodeExecutionRPCMethod(w http.ResponseWriter, r *http.Request, method string) {
	s.withEthNodeRequest(w, r, func(req ethNodeOperationRequest) {
		result, status, err := s.ethNodeExecutionRPC(r.Context(), req.Network, req.Instance, method, nil)
		if err != nil {
			http.Error(w, err.Error(), status)
			return
		}

		writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
			Kind: operations.ResultKindObject,
			Data: result,
			Meta: map[string]any{
				"network":  req.Network,
				"instance": req.Instance,
				"method":   method,
			},
		})
	})
}

func (s *service) handleEthNodeExecutionRPCStringMethod(w http.ResponseWriter, r *http.Request, method string) {
	request, err := decodeValidatedEthNodeNodeArgs(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	result, status, err := s.ethNodeExecutionRPC(r.Context(), request.Network, request.Instance, method, nil)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	value, err := operations.DecodeValue[string](result)
	if err != nil {
		http.Error(w, "unexpected JSON-RPC result shape", http.StatusBadGateway)
		return
	}

	writeObjectOperationResponse(s.log, w, http.StatusOK, value, ethNodeMeta(request, method))
}

func (s *service) handleEthNodeGetBlockByNumber(w http.ResponseWriter, r *http.Request) {
	s.withEthNodeRequest(w, r, func(req ethNodeOperationRequest) {
		block := optionalStringArg(req.Args, "block")
		if block == "" {
			block = "latest"
		}

		fullTx, _ := req.Args["full_tx"].(bool)

		result, status, err := s.ethNodeExecutionRPC(
			r.Context(),
			req.Network,
			req.Instance,
			"eth_getBlockByNumber",
			[]any{block, fullTx},
		)
		if err != nil {
			http.Error(w, err.Error(), status)
			return
		}

		writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
			Kind: operations.ResultKindObject,
			Data: result,
			Meta: map[string]any{
				"network":  req.Network,
				"instance": req.Instance,
				"method":   "eth_getBlockByNumber",
			},
		})
	})
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

func validateEthNodeNodeRequest(network, instance string) (operations.EthNodeNodeArgs, error) {
	if strings.TrimSpace(network) == "" {
		return operations.EthNodeNodeArgs{}, fmt.Errorf("network is required")
	}

	if strings.TrimSpace(instance) == "" {
		return operations.EthNodeNodeArgs{}, fmt.Errorf("instance is required")
	}

	if !ethnodeSegmentPattern.MatchString(network) {
		return operations.EthNodeNodeArgs{}, fmt.Errorf("invalid network name: must match [a-z0-9-]")
	}

	if !ethnodeSegmentPattern.MatchString(instance) {
		return operations.EthNodeNodeArgs{}, fmt.Errorf("invalid instance name: must match [a-z0-9-]")
	}

	return operations.EthNodeNodeArgs{
		Network:  network,
		Instance: instance,
	}, nil
}

func decodeValidatedEthNodeNodeArgs(r *http.Request) (operations.EthNodeNodeArgs, error) {
	request, err := decodeTypedOperationArgs[operations.EthNodeNodeArgs](r)
	if err != nil {
		return operations.EthNodeNodeArgs{}, err
	}

	return validateEthNodeNodeRequest(request.Network, request.Instance)
}

func parseEthNodePathArg(args map[string]any) (string, error) {
	path, err := requiredStringArg(args, "path")
	if err != nil {
		return "", err
	}

	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	return path, nil
}

func (s *service) withEthNodeRequest(
	w http.ResponseWriter,
	r *http.Request,
	fn func(ethNodeOperationRequest),
) {
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

	fn(ethNodeOperationRequest{
		Args:     req.Args,
		Network:  network,
		Instance: instance,
	})
}

func writeTypedEthNodeBeaconResponse[T any](
	s *service,
	w http.ResponseWriter,
	r *http.Request,
	request operations.EthNodeNodeArgs,
	path string,
) {
	data, status, err := s.ethNodeBeaconRequest(r.Context(), http.MethodGet, request.Network, request.Instance, path, nil, nil)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	payload, err := operations.DecodeValue[T](data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	writeObjectOperationResponse(s.log, w, http.StatusOK, payload, ethNodeMeta(request, path))
}

func handleTypedEthNodeBeaconResponse[T any](
	s *service,
	w http.ResponseWriter,
	r *http.Request,
	path string,
) {
	request, err := decodeValidatedEthNodeNodeArgs(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	writeTypedEthNodeBeaconResponse[T](s, w, r, request, path)
}

func ethNodeMeta(request operations.EthNodeNodeArgs, detail string) map[string]any {
	meta := map[string]any{
		"network":  request.Network,
		"instance": request.Instance,
	}

	if detail != "" {
		if strings.HasPrefix(detail, "/") {
			meta["path"] = detail
		} else {
			meta["method"] = detail
		}
	}

	return meta
}

func ethNodeHexPayload(field, hexValue string, parsedValue uint64) (any, error) {
	switch field {
	case "block_number":
		return operations.EthNodeBlockNumberPayload{
			Hex:         hexValue,
			BlockNumber: parsedValue,
		}, nil
	case "peer_count":
		return operations.EthNodePeerCountRPCPayload{
			Hex:       hexValue,
			PeerCount: parsedValue,
		}, nil
	case "chain_id":
		return operations.EthNodeChainIDPayload{
			Hex:     hexValue,
			ChainID: parsedValue,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported ethnode hex field %q", field)
	}
}

func (s *service) writeEthNodeCuratedBeaconResponse(
	w http.ResponseWriter,
	r *http.Request,
	req ethNodeOperationRequest,
	path string,
) {
	data, status, err := s.ethNodeBeaconRequest(r.Context(), http.MethodGet, req.Network, req.Instance, path, nil, nil)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: data,
		Meta: map[string]any{
			"network":  req.Network,
			"instance": req.Instance,
			"path":     path,
		},
	})
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
