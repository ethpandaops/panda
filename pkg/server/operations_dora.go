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

var doraLinkIdentifierKeys = []string{"index_or_pubkey", "slot_or_hash", "epoch", "address", "number_or_hash"}

func (s *service) registerDoraOperations() {
	s.registerOperation("dora.list_networks", func(w http.ResponseWriter, _ *http.Request) {
		s.handleDoraListNetworks(w)
	})
	s.registerOperation("dora.get_base_url", s.handleDoraBaseURL)
	s.registerOperation("dora.get_network_overview", s.handleDoraNetworkOverview)
	s.registerOperation("dora.get_validator", func(w http.ResponseWriter, r *http.Request) {
		s.handleDoraDataGetPassthrough(w, r, "index_or_pubkey", "/api/v1/validator/%s")
	})
	s.registerOperation("dora.get_validators", s.handleDoraValidators)
	s.registerOperation("dora.get_slot", func(w http.ResponseWriter, r *http.Request) {
		s.handleDoraDataGetPassthrough(w, r, "slot_or_hash", "/api/v1/slot/%s")
	})
	s.registerOperation("dora.get_epoch", func(w http.ResponseWriter, r *http.Request) {
		s.handleDoraDataGetPassthrough(w, r, "epoch", "/api/v1/epoch/%s")
	})
	s.registerOperation("dora.link_validator", func(w http.ResponseWriter, r *http.Request) {
		s.handleDoraLink(w, r, "index_or_pubkey", "/validator/%s")
	})
	s.registerOperation("dora.link_slot", func(w http.ResponseWriter, r *http.Request) {
		s.handleDoraLink(w, r, "slot_or_hash", "/slot/%s")
	})
	s.registerOperation("dora.link_epoch", func(w http.ResponseWriter, r *http.Request) {
		s.handleDoraLink(w, r, "epoch", "/epoch/%s")
	})
	s.registerOperation("dora.link_address", func(w http.ResponseWriter, r *http.Request) {
		s.handleDoraLink(w, r, "address", "/address/%s")
	})
	s.registerOperation("dora.link_block", func(w http.ResponseWriter, r *http.Request) {
		s.handleDoraLink(w, r, "number_or_hash", "/block/%s")
	})
}

func (s *service) handleDoraListNetworks(w http.ResponseWriter) {
	networks, err := s.doraNetworks()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	items := make([]operations.DoraNetwork, 0, len(networks))
	for name, baseURL := range networks {
		items = append(items, operations.DoraNetwork{
			Name:    name,
			DoraURL: baseURL,
		})
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].Name < items[j].Name
	})

	writeObjectOperationResponse(
		s.log,
		w,
		http.StatusOK,
		operations.DoraNetworksPayload{Networks: items},
		nil,
	)
}

func (s *service) handleDoraBaseURL(w http.ResponseWriter, r *http.Request) {
	request, err := decodeTypedOperationArgs[operations.DoraNetworkArgs](r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	baseURL, status, err := s.doraBaseURL(request.Network)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	writeObjectOperationResponse(
		s.log,
		w,
		http.StatusOK,
		operations.DoraBaseURLPayload{BaseURL: baseURL},
		nil,
	)
}

func (s *service) handleDoraNetworkOverview(w http.ResponseWriter, r *http.Request) {
	request, err := decodeTypedOperationArgs[operations.DoraNetworkArgs](r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	baseURL, status, err := s.doraBaseURL(request.Network)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	data, _, err := s.doraAPIGet(r.Context(), baseURL, "/api/v1/epoch/head", nil)
	if err != nil {
		data, status, err = s.doraAPIGet(r.Context(), baseURL, "/api/v1/epoch/latest", nil)
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

	writeObjectOperationResponse(
		s.log,
		w,
		http.StatusOK,
		operations.DoraOverviewPayload{
			CurrentEpoch:          overview["current_epoch"],
			CurrentSlot:           overview["current_slot"],
			Finalized:             overview["finalized"],
			ParticipationRate:     overview["participation_rate"],
			ActiveValidatorCount:  overview["active_validator_count"],
			TotalValidatorCount:   overview["total_validator_count"],
			PendingValidatorCount: overview["pending_validator_count"],
			ExitedValidatorCount:  overview["exited_validator_count"],
		},
		map[string]any{"network": request.Network},
	)
}

func (s *service) handleDoraValidators(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	baseURL, status, err := s.doraBaseURL(optionalStringArg(req.Args, "network"))
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	params := url.Values{"limit": {fmt.Sprintf("%d", optionalIntArg(req.Args, "limit", 100))}}
	if statusFilter := optionalStringArg(req.Args, "status"); statusFilter != "" {
		params.Set("status", statusFilter)
	}

	body, contentType, status, err := s.doraAPIGetRaw(r.Context(), baseURL, "/api/v1/validators", params)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	writePassthroughResponse(w, http.StatusOK, contentType, body)
}

func (s *service) handleDoraDataGetPassthrough(
	w http.ResponseWriter,
	r *http.Request,
	argName, pathTemplate string,
) {
	network, identifier, err := decodeDoraIdentifierArgs(r, argName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	baseURL, status, err := s.doraBaseURL(network)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	body, contentType, status, err := s.doraAPIGetRaw(r.Context(), baseURL, fmt.Sprintf(pathTemplate, identifier), nil)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	writePassthroughResponse(w, http.StatusOK, contentType, body)
}

func (s *service) handleDoraLink(w http.ResponseWriter, r *http.Request, argName, pathTemplate string) {
	network, identifier, err := decodeDoraIdentifierArgs(r, argName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	baseURL, status, err := s.doraBaseURL(network)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	writeObjectOperationResponse(
		s.log,
		w,
		http.StatusOK,
		operations.URLPayload{URL: strings.TrimRight(baseURL, "/") + fmt.Sprintf(pathTemplate, identifier)},
		map[string]any{"network": network},
	)
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

func (s *service) doraBaseURL(network string) (string, int, error) {
	if strings.TrimSpace(network) == "" {
		return "", http.StatusBadRequest, fmt.Errorf("network is required")
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

func decodeDoraIdentifierArgs(r *http.Request, argName string) (string, string, error) {
	switch argName {
	case "index_or_pubkey":
		request, err := decodeTypedOperationArgs[operations.DoraIndexOrPubkeyArgs](r)
		if err != nil {
			return "", "", err
		}

		if strings.TrimSpace(request.Network) == "" {
			return "", "", fmt.Errorf("network is required")
		}

		if strings.TrimSpace(request.IndexOrPubkey) == "" {
			return "", "", fmt.Errorf("index_or_pubkey is required")
		}

		return request.Network, request.IndexOrPubkey, nil
	case "slot_or_hash":
		request, err := decodeTypedOperationArgs[operations.DoraSlotOrHashArgs](r)
		if err != nil {
			return "", "", err
		}

		if strings.TrimSpace(request.Network) == "" {
			return "", "", fmt.Errorf("network is required")
		}

		if strings.TrimSpace(request.SlotOrHash) == "" {
			return "", "", fmt.Errorf("slot_or_hash is required")
		}

		return request.Network, request.SlotOrHash, nil
	case "epoch":
		request, err := decodeTypedOperationArgs[operations.DoraEpochArgs](r)
		if err != nil {
			return "", "", err
		}

		if strings.TrimSpace(request.Network) == "" {
			return "", "", fmt.Errorf("network is required")
		}

		if strings.TrimSpace(request.Epoch) == "" {
			return "", "", fmt.Errorf("epoch is required")
		}

		return request.Network, request.Epoch, nil
	case "address":
		request, err := decodeTypedOperationArgs[operations.DoraAddressArgs](r)
		if err != nil {
			return "", "", err
		}

		if strings.TrimSpace(request.Network) == "" {
			return "", "", fmt.Errorf("network is required")
		}

		if strings.TrimSpace(request.Address) == "" {
			return "", "", fmt.Errorf("address is required")
		}

		return request.Network, request.Address, nil
	case "number_or_hash":
		request, err := decodeTypedOperationArgs[operations.DoraNumberOrHashArgs](r)
		if err != nil {
			return "", "", err
		}

		if strings.TrimSpace(request.Network) == "" {
			return "", "", fmt.Errorf("network is required")
		}

		if strings.TrimSpace(request.NumberOrHash) == "" {
			return "", "", fmt.Errorf("number_or_hash is required")
		}

		return request.Network, request.NumberOrHash, nil
	default:
		return "", "", fmt.Errorf("unsupported Dora identifier %q", argName)
	}
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
		return nil, "", resp.StatusCode, fmt.Errorf(
			"%s",
			upstreamFailureMessage(
				"dora.http",
				resp.StatusCode,
				body,
				"url="+requestURL,
			),
		)
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
