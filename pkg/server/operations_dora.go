package server

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
	"time"

	"github.com/ethpandaops/panda/pkg/operations"
)

// defaultSlotsPerEpoch is the mainnet SLOTS_PER_EPOCH, used when Dora does not
// report the network's actual value.
const defaultSlotsPerEpoch = 32

func (s *service) handleDoraOperation(operationID string, w http.ResponseWriter, r *http.Request) bool {
	switch operationID {
	case "dora.list_networks":
		s.handleDoraListNetworks(w)
	case "dora.get_base_url":
		s.handleDoraBaseURL(w, r)
	case "dora.get_network_overview":
		s.handleDoraNetworkOverview(w, r)
	case "dora.get_validator":
		s.handleDoraDataGetPassthrough(w, r, "index_or_pubkey", "/api/v1/validator/%s")
	case "dora.get_validators":
		s.handleDoraValidators(w, r)
	case "dora.get_slot":
		s.handleDoraDataGetPassthrough(w, r, "slot_or_hash", "/api/v1/slot/%s")
	case "dora.get_epoch":
		s.handleDoraDataGetPassthrough(w, r, "epoch", "/api/v1/epoch/%s")
	case "dora.link_validator":
		s.handleDoraLink(w, r, "/validator/%s")
	case "dora.link_slot":
		s.handleDoraLink(w, r, "/slot/%s")
	case "dora.link_epoch":
		s.handleDoraLink(w, r, "/epoch/%s")
	case "dora.link_address":
		s.handleDoraLink(w, r, "/address/%s")
	case "dora.link_block":
		s.handleDoraLink(w, r, "/block/%s")
	default:
		return false
	}

	return true
}

func (s *service) handleDoraListNetworks(w http.ResponseWriter) {
	networks, err := s.doraNetworks()
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	items := make([]listItem, 0, len(networks))
	for name, baseURL := range networks {
		items = append(items, listItem{
			Name: name,
			URL:  baseURL,
			Type: "dora",
		})
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].Name < items[j].Name
	})

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: map[string]any{"networks": items},
	})
}

func (s *service) handleDoraBaseURL(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	baseURL, status, err := s.doraBaseURL(req.Args)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: map[string]any{"base_url": baseURL},
	})
}

func (s *service) handleDoraNetworkOverview(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	baseURL, status, err := s.doraBaseURL(req.Args)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	data, status, err := s.doraAPIGet(r.Context(), baseURL, "/api/v1/network/overview", nil)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	payload, err := doraDataObject(data, "network overview")
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, err.Error())
		return
	}

	overview, ok := doraOverview(payload)
	if !ok {
		writeAPIError(
			w,
			http.StatusBadGateway,
			"Dora network overview is missing summary fields; upgrade Dora to a release whose /api/v1/network/overview includes them",
		)

		return
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: overview,
		Meta: map[string]any{"network": optionalStringArg(req.Args, "network")},
	})
}

func (s *service) handleDoraValidators(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	baseURL, status, err := s.doraBaseURL(req.Args)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	params := url.Values{"limit": {fmt.Sprintf("%d", optionalIntArg(req.Args, "limit", 100))}}
	if statusFilter := optionalStringArg(req.Args, "status"); statusFilter != "" {
		params.Set("status", statusFilter)
	}

	body, contentType, status, err := s.doraAPIGetRaw(r.Context(), baseURL, "/api/v1/validators", params)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	writePassthroughResponse(w, http.StatusOK, contentType, body)
}

func (s *service) handleDoraDataGetPassthrough(
	w http.ResponseWriter,
	r *http.Request,
	argName, pathTemplate string,
) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	baseURL, status, err := s.doraBaseURL(req.Args)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	identifier, err := requiredStringArg(req.Args, argName)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	body, contentType, status, err := s.doraAPIGetRaw(r.Context(), baseURL, fmt.Sprintf(pathTemplate, url.PathEscape(identifier)), nil)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	writePassthroughResponse(w, http.StatusOK, contentType, body)
}

func (s *service) handleDoraLink(w http.ResponseWriter, r *http.Request, pathTemplate string) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	baseURL, status, err := s.doraBaseURL(req.Args)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	identifier := ""
	for _, key := range []string{"index_or_pubkey", "slot_or_hash", "epoch", "address", "number_or_hash"} {
		if value := optionalStringArg(req.Args, key); value != "" {
			identifier = value
			break
		}
	}
	if identifier == "" {
		writeAPIError(w, http.StatusBadRequest, "identifier is required")
		return
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: map[string]any{"url": strings.TrimRight(baseURL, "/") + fmt.Sprintf(pathTemplate, url.PathEscape(identifier))},
		Meta: map[string]any{"network": optionalStringArg(req.Args, "network")},
	})
}

func (s *service) doraNetworks() (map[string]string, error) {
	if s.cartographoorClient == nil {
		return nil, fmt.Errorf("dora is unavailable")
	}

	networks := make(map[string]string)
	for name, network := range s.cartographoorClient.GetActiveNetworks() {
		if network.ServiceURLs != nil && network.ServiceURLs.Dora != "" {
			networks[name] = network.ServiceURLs.Dora
		}
	}

	return networks, nil
}

func (s *service) doraBaseURL(args map[string]any) (string, int, error) {
	network, err := requiredStringArg(args, "network")
	if err != nil {
		return "", http.StatusBadRequest, err
	}

	networks, err := s.doraNetworks()
	if err != nil {
		return "", http.StatusServiceUnavailable, err
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

func (s *service) doraAPIGet(
	ctx context.Context,
	baseURL, path string,
	params url.Values,
) (map[string]any, int, error) {
	body, _, status, err := s.doraAPIGetRaw(ctx, baseURL, path, params)
	if err != nil {
		return nil, status, err
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("invalid Dora JSON response: %w", err)
	}
	if status, ok := payload["status"].(string); ok && status != "" && !strings.EqualFold(status, "OK") {
		return nil, http.StatusBadGateway, fmt.Errorf("dora API error: %s", status)
	}

	return payload, http.StatusOK, nil
}

func (s *service) doraAPIGetRaw(
	ctx context.Context,
	baseURL, path string,
	params url.Values,
) ([]byte, string, int, error) {
	requestURL := strings.TrimRight(baseURL, "/") + path
	if len(params) > 0 {
		requestURL += "?" + params.Encode()
	}

	requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, "", http.StatusInternalServerError, fmt.Errorf("creating Dora request: %w", err)
	}

	resp, err := s.httpClient.Do(req)
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

func epochStartSlot(value any, slotsPerEpoch int64) any {
	switch epoch := value.(type) {
	case float64:
		if epoch != float64(int64(epoch)) {
			return value
		}
		return int64(epoch) * slotsPerEpoch
	case json.Number:
		if parsed, err := epoch.Int64(); err == nil {
			return parsed * slotsPerEpoch
		}
	case string:
		if parsed, err := strconv.ParseInt(epoch, 10, 64); err == nil {
			return parsed * slotsPerEpoch
		}
	}

	return value
}

// doraOverview normalizes a Dora /api/v1/network/overview payload into the
// overview shape exposed to clients. It reports false when the payload lacks
// the flattened summary fields (older Dora releases).
func doraOverview(payload map[string]any) (map[string]any, bool) {
	if _, ok := numericValue(payload["current_epoch"]); !ok {
		return nil, false
	}

	overview := make(map[string]any, len(payload)+2)
	for key, value := range payload {
		overview[key] = value
	}

	slots := doraSlotsPerEpoch(payload)
	overview["current_epoch_start_slot"] = epochStartSlot(payload["current_epoch"], slots)

	if _, ok := numericValue(payload["finalized_epoch"]); ok {
		overview["finalized_epoch_start_slot"] = epochStartSlot(payload["finalized_epoch"], slots)
	}

	return overview, true
}

// doraSlotsPerEpoch reads the network's SLOTS_PER_EPOCH from the overview
// payload, falling back to the mainnet default when it is not reported.
func doraSlotsPerEpoch(payload map[string]any) int64 {
	for _, key := range []string{"current_state", "metadata"} {
		section, ok := payload[key].(map[string]any)
		if !ok {
			continue
		}

		if value, ok := numericValue(section["slots_per_epoch"]); ok && value > 0 {
			return int64(value)
		}
	}

	return defaultSlotsPerEpoch
}

func doraDataObject(payload map[string]any, label string) (map[string]any, error) {
	data, ok := payload["data"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("invalid Dora %s response: data is not an object", label)
	}

	return data, nil
}

// numericValue parses a number from a decoded Dora JSON payload, which only
// ever yields float64, json.Number, or stringly-typed numbers.
func numericValue(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case json.Number:
		parsed, err := number.Float64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseFloat(number, 64)
		return parsed, err == nil
	}

	return 0, false
}
