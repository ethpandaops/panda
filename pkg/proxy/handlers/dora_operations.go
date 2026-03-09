package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ethpandaops/cartographoor/pkg/discovery"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/mcp/pkg/cartographoor"
	"github.com/ethpandaops/mcp/pkg/operations"
)

type DoraOperationsHandler struct {
	log         logrus.FieldLogger
	httpClient  *http.Client
	networksURL string
	cacheTTL    time.Duration

	mu           sync.RWMutex
	lastRefresh  time.Time
	doraNetworks map[string]string
}

func NewDoraOperationsHandler(log logrus.FieldLogger) *DoraOperationsHandler {
	return &DoraOperationsHandler{
		log:          log.WithField("handler", "dora-operations"),
		httpClient:   &http.Client{Timeout: cartographoor.DefaultHTTPTimeout},
		networksURL:  cartographoor.DefaultCartographoorURL,
		cacheTTL:     cartographoor.DefaultCacheTTL,
		doraNetworks: map[string]string{},
	}
}

func (h *DoraOperationsHandler) HandleOperation(operationID string, w http.ResponseWriter, r *http.Request) bool {
	switch operationID {
	case "dora.list_networks":
		h.handleListNetworks(w, r)
	case "dora.get_base_url":
		h.handleBaseURL(w, r)
	case "dora.get_network_overview":
		h.handleNetworkOverview(w, r)
	case "dora.get_validator":
		h.handleValidator(w, r)
	case "dora.get_validators":
		h.handleValidators(w, r)
	case "dora.get_slot":
		h.handleSlot(w, r)
	case "dora.get_epoch":
		h.handleEpoch(w, r)
	case "dora.link_validator":
		h.handleLink(w, r, "/validator/%s")
	case "dora.link_slot":
		h.handleLink(w, r, "/slot/%s")
	case "dora.link_epoch":
		h.handleLink(w, r, "/epoch/%s")
	case "dora.link_address":
		h.handleLink(w, r, "/address/%s")
	case "dora.link_block":
		h.handleLink(w, r, "/block/%s")
	default:
		return false
	}

	return true
}

func (h *DoraOperationsHandler) handleListNetworks(w http.ResponseWriter, r *http.Request) {
	networks, status, err := h.getNetworks(r.Context())
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	items := make([]map[string]any, 0, len(networks))
	for name, baseURL := range networks {
		items = append(items, map[string]any{
			"name":     name,
			"dora_url": baseURL,
		})
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i]["name"].(string) < items[j]["name"].(string)
	})

	writeOperationResponse(h.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: map[string]any{"networks": items},
	})
}

func (h *DoraOperationsHandler) handleBaseURL(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	baseURL, status, err := h.getBaseURL(r.Context(), req.Args)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	writeOperationResponse(h.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: map[string]any{"base_url": baseURL},
	})
}

func (h *DoraOperationsHandler) handleNetworkOverview(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	baseURL, status, err := h.getBaseURL(r.Context(), req.Args)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	data, status, err := h.apiGet(r.Context(), baseURL, "/api/v1/epoch/head", nil)
	if err != nil {
		data, status, err = h.apiGet(r.Context(), baseURL, "/api/v1/epoch/latest", nil)
		if err != nil {
			http.Error(w, err.Error(), status)
			return
		}
	}

	payload, _ := data["data"].(map[string]any)
	overview := map[string]any{
		"current_epoch":      payload["epoch"],
		"current_slot":       multiplyEpoch(payload["epoch"]),
		"finalized":          payload["finalized"],
		"participation_rate": payload["globalparticipationrate"],
	}
	if validatorInfo, ok := payload["validatorinfo"].(map[string]any); ok {
		overview["active_validator_count"] = validatorInfo["active"]
		overview["total_validator_count"] = validatorInfo["total"]
		overview["pending_validator_count"] = validatorInfo["pending"]
		overview["exited_validator_count"] = validatorInfo["exited"]
	}

	writeOperationResponse(h.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: overview,
		Meta: map[string]any{"network": optionalStringArg(req.Args, "network")},
	})
}

func (h *DoraOperationsHandler) handleValidator(w http.ResponseWriter, r *http.Request) {
	h.handleDataGetPassthrough(w, r, "index_or_pubkey", "/api/v1/validator/%s")
}

func (h *DoraOperationsHandler) handleSlot(w http.ResponseWriter, r *http.Request) {
	h.handleDataGetPassthrough(w, r, "slot_or_hash", "/api/v1/slot/%s")
}

func (h *DoraOperationsHandler) handleEpoch(w http.ResponseWriter, r *http.Request) {
	h.handleDataGetPassthrough(w, r, "epoch", "/api/v1/epoch/%s")
}

func (h *DoraOperationsHandler) handleValidators(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	baseURL, status, err := h.getBaseURL(r.Context(), req.Args)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	params := url.Values{
		"limit": {fmt.Sprintf("%d", optionalIntArg(req.Args, "limit", 100))},
	}
	if statusFilter := optionalStringArg(req.Args, "status"); statusFilter != "" {
		params.Set("status", statusFilter)
	}

	body, contentType, status, err := h.apiGetRaw(r.Context(), baseURL, "/api/v1/validators", params)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	writePassthroughResponse(w, http.StatusOK, contentType, body)
}

func (h *DoraOperationsHandler) handleDataGetPassthrough(
	w http.ResponseWriter,
	r *http.Request,
	argName, pathTemplate string,
) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	baseURL, status, err := h.getBaseURL(r.Context(), req.Args)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	identifier, err := requiredStringArg(req.Args, argName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	body, contentType, status, err := h.apiGetRaw(r.Context(), baseURL, fmt.Sprintf(pathTemplate, identifier), nil)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	writePassthroughResponse(w, http.StatusOK, contentType, body)
}

func (h *DoraOperationsHandler) handleLink(w http.ResponseWriter, r *http.Request, pathTemplate string) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	baseURL, status, err := h.getBaseURL(r.Context(), req.Args)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	var identifier string
	for _, key := range []string{"index_or_pubkey", "slot_or_hash", "epoch", "address", "number_or_hash"} {
		if value := optionalStringArg(req.Args, key); value != "" {
			identifier = value
			break
		}
	}
	if identifier == "" {
		http.Error(w, "identifier is required", http.StatusBadRequest)
		return
	}

	writeOperationResponse(h.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: map[string]any{"url": strings.TrimRight(baseURL, "/") + fmt.Sprintf(pathTemplate, identifier)},
		Meta: map[string]any{"network": optionalStringArg(req.Args, "network")},
	})
}

func (h *DoraOperationsHandler) getBaseURL(ctx context.Context, args map[string]any) (string, int, error) {
	network, err := requiredStringArg(args, "network")
	if err != nil {
		return "", http.StatusBadRequest, err
	}

	networks, status, err := h.getNetworks(ctx)
	if err != nil {
		return "", status, err
	}

	baseURL, ok := networks[network]
	if !ok {
		names := make([]string, 0, len(networks))
		for name := range networks {
			names = append(names, name)
		}
		sort.Strings(names)
		return "", http.StatusNotFound, fmt.Errorf("unknown network %q. Available: %v", network, names)
	}

	return baseURL, http.StatusOK, nil
}

func (h *DoraOperationsHandler) getNetworks(ctx context.Context) (map[string]string, int, error) {
	h.mu.RLock()
	if len(h.doraNetworks) > 0 && time.Since(h.lastRefresh) < h.cacheTTL {
		networks := mapsClone(h.doraNetworks)
		h.mu.RUnlock()
		return networks, http.StatusOK, nil
	}
	h.mu.RUnlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.networksURL, nil)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("creating cartographoor request: %w", err)
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("fetching networks: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var result discovery.Result
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("decoding networks response: %w", err)
	}

	networks := make(map[string]string)
	for name, network := range result.Networks {
		if network.Status == "active" && network.ServiceURLs != nil && network.ServiceURLs.Dora != "" {
			networks[name] = network.ServiceURLs.Dora
		}
	}

	h.mu.Lock()
	h.doraNetworks = mapsClone(networks)
	h.lastRefresh = time.Now()
	h.mu.Unlock()

	return networks, http.StatusOK, nil
}

func (h *DoraOperationsHandler) apiGet(
	ctx context.Context,
	baseURL, path string,
	params url.Values,
) (map[string]any, int, error) {
	body, _, status, err := h.apiGetRaw(ctx, baseURL, path, params)
	if err != nil {
		return nil, status, err
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("invalid Dora JSON response: %w", err)
	}

	return payload, http.StatusOK, nil
}

func (h *DoraOperationsHandler) apiGetRaw(
	ctx context.Context,
	baseURL, path string,
	params url.Values,
) ([]byte, string, int, error) {
	requestURL := strings.TrimRight(baseURL, "/") + path
	if len(params) > 0 {
		requestURL += "?" + params.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, "", http.StatusInternalServerError, fmt.Errorf("creating Dora request: %w", err)
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, "", http.StatusBadGateway, fmt.Errorf("executing Dora request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", http.StatusBadGateway, fmt.Errorf("reading Dora response: %w", err)
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

func multiplyEpoch(value any) any {
	switch epoch := value.(type) {
	case float64:
		return epoch * 32
	case json.Number:
		if parsed, err := epoch.Int64(); err == nil {
			return parsed * 32
		}
	case string:
		if parsed, err := strconv.ParseInt(epoch, 10, 64); err == nil {
			return parsed * 32
		}
	}

	return value
}

func mapsClone(input map[string]string) map[string]string {
	cloned := make(map[string]string, len(input))
	for key, value := range input {
		cloned[key] = value
	}

	return cloned
}
