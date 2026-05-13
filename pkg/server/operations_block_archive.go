package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	blockarchive "github.com/ethpandaops/panda/modules/block_archive"
	"github.com/ethpandaops/panda/pkg/operations"
)

const (
	blockArchiveNetworksTTL  = 5 * time.Minute
	blockArchiveHTTPTimeout  = 30 * time.Second
	blockArchiveMaxBlockSize = 50 * 1024 * 1024
	blockArchiveMaxMetaSize  = 1 * 1024 * 1024
)

type blockArchiveNetwork struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Source     string `json:"source"`
	TracoorURL string `json:"tracoor_url,omitempty"`
	ChainID    *int64 `json:"chain_id,omitempty"`
	Polling    bool   `json:"polling"`
}

// blockArchiveNetworksCache caches the supported-networks list fetched from
// the block-archiver's /api/v1/networks endpoint.
type blockArchiveNetworksCache struct {
	mu        sync.Mutex
	networks  []blockArchiveNetwork
	fetchedAt time.Time
}

func (s *service) handleBlockArchiveOperation(operationID string, w http.ResponseWriter, r *http.Request) bool {
	switch operationID {
	case "block_archive.list_networks":
		s.handleBlockArchiveListNetworks(w, r)
	case "block_archive.get_base_url":
		s.handleBlockArchiveBaseURL(w)
	case "block_archive.download_ssz":
		s.handleBlockArchiveDownloadSSZ(w, r)
	case "block_archive.get_block_json":
		s.handleBlockArchiveBlockJSON(w, r)
	case "block_archive.link":
		s.handleBlockArchiveLink(w, r)
	default:
		return false
	}

	return true
}

func (s *service) handleBlockArchiveListNetworks(w http.ResponseWriter, r *http.Request) {
	baseURL, status, err := s.blockArchiveBaseURL()
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	activeFilter, err := optionalBoolArg(req.Args, "active")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	networks, err := s.blockArchiveNetworks(r.Context(), baseURL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	if activeFilter != nil {
		filtered := make([]blockArchiveNetwork, 0, len(networks))
		for _, n := range networks {
			if n.Polling == *activeFilter {
				filtered = append(filtered, n)
			}
		}
		networks = filtered
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: map[string]any{"networks": networks},
	})
}

func (s *service) handleBlockArchiveBaseURL(w http.ResponseWriter) {
	baseURL, status, err := s.blockArchiveBaseURL()
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: map[string]any{"base_url": baseURL},
	})
}

func (s *service) handleBlockArchiveDownloadSSZ(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	baseURL, network, slot, blockRoot, status, err := s.blockArchiveParams(req.Args)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	path := fmt.Sprintf("/%s/%d/%s.ssz", network, slot, blockRoot)
	body, _, status, err := s.blockArchiveGetRaw(r.Context(), baseURL, path, blockArchiveMaxBlockSize)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	writePassthroughResponse(w, http.StatusOK, "application/octet-stream", body)
}

func (s *service) handleBlockArchiveBlockJSON(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	baseURL, network, slot, blockRoot, status, err := s.blockArchiveParams(req.Args)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	path := fmt.Sprintf("/%s/%d/%s.json", network, slot, blockRoot)
	body, _, status, err := s.blockArchiveGetRaw(r.Context(), baseURL, path, blockArchiveMaxBlockSize)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	writePassthroughResponse(w, http.StatusOK, "application/json", body)
}

func (s *service) handleBlockArchiveLink(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	baseURL, network, slot, blockRoot, status, err := s.blockArchiveParams(req.Args)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	url := fmt.Sprintf("%s/%s/%d/", strings.TrimRight(baseURL, "/"), network, slot)

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: map[string]any{
			"url":          url,
			"download_url": fmt.Sprintf("%s/%s/%d/%s.ssz", strings.TrimRight(baseURL, "/"), network, slot, blockRoot),
		},
		Meta: map[string]any{"network": network},
	})
}

func (s *service) blockArchiveBaseURL() (string, int, error) {
	if s.moduleRegistry == nil {
		return "", http.StatusServiceUnavailable, errors.New("block archive is unavailable")
	}

	ext := s.moduleRegistry.Get("block_archive")
	if ext == nil {
		return "", http.StatusServiceUnavailable, errors.New("block archive module is not initialized")
	}

	mod, ok := ext.(*blockarchive.Module)
	if !ok || !mod.Enabled() {
		return "", http.StatusServiceUnavailable, errors.New("block archive is not enabled")
	}

	baseURL := strings.TrimRight(mod.URL(), "/")
	if baseURL == "" {
		return "", http.StatusServiceUnavailable, errors.New("block archive URL is not configured")
	}

	return baseURL, http.StatusOK, nil
}

func (s *service) blockArchiveParams(args map[string]any) (string, string, int64, string, int, error) {
	baseURL, status, err := s.blockArchiveBaseURL()
	if err != nil {
		return "", "", 0, "", status, err
	}

	network, err := requiredStringArg(args, "network")
	if err != nil {
		return "", "", 0, "", http.StatusBadRequest, err
	}

	if !isSafeNetworkName(network) {
		return "", "", 0, "", http.StatusBadRequest, fmt.Errorf("invalid network name %q", network)
	}

	slot, err := requiredSlotArg(args, "slot")
	if err != nil {
		return "", "", 0, "", http.StatusBadRequest, err
	}

	blockRoot, err := requiredStringArg(args, "block_root")
	if err != nil {
		return "", "", 0, "", http.StatusBadRequest, err
	}

	blockRoot = strings.ToLower(blockRoot)
	if !isBlockRoot(blockRoot) {
		return "", "", 0, "", http.StatusBadRequest, fmt.Errorf("invalid block_root %q (want 0x + 64 hex chars)", blockRoot)
	}

	return baseURL, network, slot, blockRoot, http.StatusOK, nil
}

func (s *service) blockArchiveNetworks(ctx context.Context, baseURL string) ([]blockArchiveNetwork, error) {
	cache := s.blockArchiveCache()
	cache.mu.Lock()
	defer cache.mu.Unlock()

	if time.Since(cache.fetchedAt) < blockArchiveNetworksTTL && cache.networks != nil {
		return append([]blockArchiveNetwork(nil), cache.networks...), nil
	}

	body, _, _, err := s.blockArchiveGetRaw(ctx, baseURL, "/api/v1/networks", blockArchiveMaxMetaSize)
	if err != nil {
		if cache.networks != nil {
			return append([]blockArchiveNetwork(nil), cache.networks...), nil
		}

		return nil, err
	}

	var payload struct {
		Networks []blockArchiveNetwork `json:"networks"`
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decoding block archive networks: %w", err)
	}

	cache.networks = append([]blockArchiveNetwork(nil), payload.Networks...)
	cache.fetchedAt = time.Now()

	return append([]blockArchiveNetwork(nil), cache.networks...), nil
}

func (s *service) blockArchiveGetRaw(ctx context.Context, baseURL, path string, maxBytes int64) ([]byte, string, int, error) {
	requestURL := strings.TrimRight(baseURL, "/") + path

	requestCtx, cancel := context.WithTimeout(ctx, blockArchiveHTTPTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, "", http.StatusInternalServerError, fmt.Errorf("creating block archive request: %w", err)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, "", http.StatusBadGateway, fmt.Errorf("executing block archive request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	reader := io.Reader(resp.Body)
	if maxBytes > 0 {
		reader = io.LimitReader(resp.Body, maxBytes+1)
	}

	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, "", http.StatusBadGateway, fmt.Errorf("reading block archive response: %w", err)
	}

	if maxBytes > 0 && int64(len(body)) > maxBytes {
		return nil, "", http.StatusBadGateway, fmt.Errorf("block archive response exceeded %d bytes", maxBytes)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", resp.StatusCode, fmt.Errorf("%s", strings.TrimSpace(string(body)))
	}

	return body, resp.Header.Get("Content-Type"), http.StatusOK, nil
}

func (s *service) blockArchiveCache() *blockArchiveNetworksCache {
	s.blockArchiveCacheOnce.Do(func() {
		s.blockArchiveNetworksCacheInst = &blockArchiveNetworksCache{}
	})

	return s.blockArchiveNetworksCacheInst
}

func requiredSlotArg(args map[string]any, key string) (int64, error) {
	parsed, err := parseSlotArg(args[key], key)
	if err != nil {
		return 0, err
	}

	if parsed < 0 {
		return 0, fmt.Errorf("%s must be non-negative", key)
	}

	return parsed, nil
}

func parseSlotArg(raw any, key string) (int64, error) {
	switch value := raw.(type) {
	case float64:
		if value != float64(int64(value)) {
			return 0, fmt.Errorf("%s must be an integer", key)
		}

		return int64(value), nil
	case int:
		return int64(value), nil
	case int64:
		return value, nil
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return 0, fmt.Errorf("%s must be an integer: %w", key, err)
		}

		return parsed, nil
	case string:
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%s must be an integer: %w", key, err)
		}

		return parsed, nil
	default:
		return 0, fmt.Errorf("%s is required", key)
	}
}

func optionalBoolArg(args map[string]any, key string) (*bool, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil, nil
	}

	switch v := raw.(type) {
	case bool:
		return &v, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "yes":
			t := true
			return &t, nil
		case "false", "0", "no":
			f := false
			return &f, nil
		}
		return nil, fmt.Errorf("%s must be a boolean", key)
	default:
		return nil, fmt.Errorf("%s must be a boolean", key)
	}
}

func isSafeNetworkName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}

	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return false
		}
	}

	return true
}

func isBlockRoot(value string) bool {
	if len(value) != 66 || !strings.HasPrefix(value, "0x") {
		return false
	}

	for _, r := range value[2:] {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}

	return true
}
