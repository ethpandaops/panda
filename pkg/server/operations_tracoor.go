package server

import (
	"bytes"
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

// defaultTracoorListLimit caps listings unless the caller asks for more,
// matching the Tracoor API's own default page size.
const defaultTracoorListLimit = 100

// tracoorArtifact describes one Tracoor artifact type: its API routes and
// the filter/unique-value fields the Tracoor spec declares for it.
type tracoorArtifact struct {
	listPath      string
	countPath     string
	uniquePath    string
	stringFilters []string
	numberFilters []string
	uniqueFields  []string
}

// tracoorArtifacts maps artifact type names (also the Tracoor UI route and
// /download/ path segments) to their API surface.
var tracoorArtifacts = map[string]tracoorArtifact{
	"beacon_state": {
		listPath:      "/v1/api/list-beacon-state",
		countPath:     "/v1/api/count-beacon-state",
		uniquePath:    "/v1/api/list-unique-beacon-state-values",
		stringFilters: []string{"node", "state_root", "node_version", "beacon_implementation", "before", "after"},
		numberFilters: []string{"slot", "epoch"},
		uniqueFields:  []string{"node", "slot", "epoch", "state_root", "node_version", "network", "beacon_implementation"},
	},
	"beacon_block": {
		listPath:      "/v1/api/list-beacon-block",
		countPath:     "/v1/api/count-beacon-block",
		uniquePath:    "/v1/api/list-unique-beacon-block-values",
		stringFilters: []string{"node", "block_root", "node_version", "beacon_implementation", "before", "after"},
		numberFilters: []string{"slot", "epoch"},
		uniqueFields:  []string{"node", "slot", "epoch", "block_root", "node_version", "network", "beacon_implementation"},
	},
	"beacon_bad_block": {
		listPath:      "/v1/api/list-beacon-bad-block",
		countPath:     "/v1/api/count-beacon-bad-block",
		uniquePath:    "/v1/api/list-unique-beacon-bad-block-values",
		stringFilters: []string{"node", "block_root", "node_version", "beacon_implementation", "before", "after"},
		numberFilters: []string{"slot", "epoch"},
		uniqueFields:  []string{"node", "slot", "epoch", "block_root", "node_version", "network", "beacon_implementation"},
	},
	"beacon_bad_blob": {
		listPath:      "/v1/api/list-beacon-bad-blob",
		countPath:     "/v1/api/count-beacon-bad-blob",
		uniquePath:    "/v1/api/list-unique-beacon-bad-blob-values",
		stringFilters: []string{"node", "block_root", "node_version", "beacon_implementation", "before", "after"},
		numberFilters: []string{"slot", "epoch", "index"},
		uniqueFields:  []string{"node", "slot", "epoch", "block_root", "node_version", "network", "beacon_implementation", "index"},
	},
	"execution_block_trace": {
		listPath:      "/v1/api/list-execution-block-trace",
		countPath:     "/v1/api/count-execution-block-trace",
		uniquePath:    "/v1/api/list-unique-execution-block-trace-values",
		stringFilters: []string{"node", "block_hash", "node_version", "execution_implementation", "before", "after"},
		numberFilters: []string{"block_number"},
		uniqueFields:  []string{"node", "block_hash", "block_number", "network", "node_version", "execution_implementation"},
	},
	"execution_bad_block": {
		listPath:      "/v1/api/list-execution-bad-block",
		countPath:     "/v1/api/count-execution-bad-block",
		uniquePath:    "/v1/api/list-unique-execution-bad-block-values",
		stringFilters: []string{"node", "block_hash", "block_extra_data", "node_version", "execution_implementation", "before", "after"},
		numberFilters: []string{"block_number"},
		uniqueFields:  []string{"node", "block_hash", "block_number", "network", "node_version", "execution_implementation", "block_extra_data"},
	},
}

func (s *service) handleTracoorOperation(operationID string, w http.ResponseWriter, r *http.Request) bool {
	switch operationID {
	case "tracoor.list_networks":
		s.handleTracoorListNetworks(w)
	case "tracoor.get_base_url":
		s.handleTracoorBaseURL(w, r)
	case "tracoor.get_config":
		s.handleTracoorGetConfig(w, r)
	case "tracoor.list_artifacts":
		s.handleTracoorListArtifacts(w, r)
	case "tracoor.count_artifacts":
		s.handleTracoorCountArtifacts(w, r)
	case "tracoor.list_unique_values":
		s.handleTracoorListUniqueValues(w, r)
	case "tracoor.download_url":
		s.handleTracoorDownloadURL(w, r)
	case "tracoor.link_artifact":
		s.handleTracoorLinkArtifact(w, r)
	default:
		return false
	}

	return true
}

func (s *service) handleTracoorListNetworks(w http.ResponseWriter) {
	networks, err := s.tracoorNetworks()
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	items := make([]listItem, 0, len(networks))
	for name, baseURL := range networks {
		items = append(items, listItem{
			Name: name,
			URL:  baseURL,
			Type: "tracoor",
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

func (s *service) handleTracoorBaseURL(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	baseURL, status, err := s.tracoorBaseURL(req.Args)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: map[string]any{"base_url": baseURL},
	})
}

func (s *service) handleTracoorGetConfig(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	baseURL, status, err := s.tracoorBaseURL(req.Args)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	body, contentType, status, err := s.tracoorAPIPost(r.Context(), baseURL, "/v1/api/get-config", map[string]any{})
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	writePassthroughResponse(w, http.StatusOK, contentType, body)
}

// handleTracoorListArtifacts serves tracoor.list_artifacts: a filtered,
// paginated listing of one artifact type. The Tracoor response is passed
// through as-is ({"beacon_states": [...]}-style, uint64s as strings).
func (s *service) handleTracoorListArtifacts(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	baseURL, status, err := s.tracoorBaseURL(req.Args)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	artifact, spec, err := tracoorArtifactSpec(req.Args)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	payload, err := tracoorFilterPayload(req.Args, spec)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	if id := optionalStringArg(req.Args, "id"); id != "" {
		payload["id"] = id
	}

	pagination := map[string]any{
		"limit":  optionalIntArg(req.Args, "limit", defaultTracoorListLimit),
		"offset": optionalIntArg(req.Args, "offset", 0),
	}
	if orderBy := optionalStringArg(req.Args, "order_by"); orderBy != "" {
		pagination["order_by"] = orderBy
	}

	payload["pagination"] = pagination

	body, contentType, status, err := s.tracoorAPIPost(r.Context(), baseURL, spec.listPath, payload)
	if err != nil {
		writeAPIError(w, status, fmt.Sprintf("listing %s: %s", artifact, err))
		return
	}

	writePassthroughResponse(w, http.StatusOK, contentType, body)
}

// handleTracoorCountArtifacts serves tracoor.count_artifacts. The Tracoor
// count arrives as a quoted uint64 ({"count": "42"}); it is returned as a
// plain integer.
func (s *service) handleTracoorCountArtifacts(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	baseURL, status, err := s.tracoorBaseURL(req.Args)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	artifact, spec, err := tracoorArtifactSpec(req.Args)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	payload, err := tracoorFilterPayload(req.Args, spec)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	body, _, status, err := s.tracoorAPIPost(r.Context(), baseURL, spec.countPath, payload)
	if err != nil {
		writeAPIError(w, status, fmt.Sprintf("counting %s: %s", artifact, err))
		return
	}

	count, err := tracoorParseCount(body)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, err.Error())
		return
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: map[string]any{"artifact": artifact, "count": count},
		Meta: map[string]any{"network": optionalStringArg(req.Args, "network")},
	})
}

func (s *service) handleTracoorListUniqueValues(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	baseURL, status, err := s.tracoorBaseURL(req.Args)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	_, spec, err := tracoorArtifactSpec(req.Args)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	fields, err := tracoorUniqueFields(req.Args, spec)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	body, contentType, status, err := s.tracoorAPIPost(r.Context(), baseURL, spec.uniquePath, map[string]any{"fields": fields})
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	writePassthroughResponse(w, http.StatusOK, contentType, body)
}

// handleTracoorDownloadURL returns the URL serving an artifact's raw stored
// bytes (SSZ or JSON, possibly gzip-compressed). The URL streams the bytes
// directly or redirects to a presigned store URL.
func (s *service) handleTracoorDownloadURL(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	baseURL, status, err := s.tracoorBaseURL(req.Args)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	artifact, _, err := tracoorArtifactSpec(req.Args)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	id, err := requiredStringArg(req.Args, "id")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: map[string]any{
			"url": strings.TrimRight(baseURL, "/") + "/download/" + artifact + "/" + url.PathEscape(id),
		},
		Meta: map[string]any{"network": optionalStringArg(req.Args, "network")},
	})
}

// handleTracoorLinkArtifact builds a deep link into the Tracoor UI: the
// artifact listing view, or a single capture when an ID is given.
func (s *service) handleTracoorLinkArtifact(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	baseURL, status, err := s.tracoorBaseURL(req.Args)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	artifact, _, err := tracoorArtifactSpec(req.Args)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	link := strings.TrimRight(baseURL, "/") + "/" + artifact
	if id := optionalStringArg(req.Args, "id"); id != "" {
		link += "/" + url.PathEscape(id)
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: map[string]any{"url": link},
		Meta: map[string]any{"network": optionalStringArg(req.Args, "network")},
	})
}

func (s *service) tracoorNetworks() (map[string]string, error) {
	if s.cartographoorClient == nil {
		return nil, fmt.Errorf("tracoor is unavailable")
	}

	networks := make(map[string]string)
	for name, network := range s.cartographoorClient.GetActiveNetworks() {
		if network.ServiceURLs != nil && network.ServiceURLs.Tracoor != "" {
			networks[name] = network.ServiceURLs.Tracoor
		}
	}

	return networks, nil
}

func (s *service) tracoorBaseURL(args map[string]any) (string, int, error) {
	network, err := requiredStringArg(args, "network")
	if err != nil {
		return "", http.StatusBadRequest, err
	}

	networks, err := s.tracoorNetworks()
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

// tracoorAPIPost issues a JSON POST to the Tracoor grpc-gateway API. Every
// Tracoor API route is a POST with a JSON body.
func (s *service) tracoorAPIPost(
	ctx context.Context,
	baseURL, path string,
	payload any,
) ([]byte, string, int, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, "", http.StatusInternalServerError, fmt.Errorf("encoding Tracoor request: %w", err)
	}

	requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	requestURL := strings.TrimRight(baseURL, "/") + path

	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, requestURL, bytes.NewReader(encoded))
	if err != nil {
		return nil, "", http.StatusInternalServerError, fmt.Errorf("creating Tracoor request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, "", http.StatusBadGateway, fmt.Errorf("executing Tracoor request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", http.StatusBadGateway, fmt.Errorf("reading Tracoor response: %w", err)
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

// tracoorArtifactSpec resolves the required "artifact" arg to its spec.
func tracoorArtifactSpec(args map[string]any) (string, tracoorArtifact, error) {
	artifact, err := requiredStringArg(args, "artifact")
	if err != nil {
		return "", tracoorArtifact{}, err
	}

	spec, ok := tracoorArtifacts[artifact]
	if !ok {
		names := make([]string, 0, len(tracoorArtifacts))
		for name := range tracoorArtifacts {
			names = append(names, name)
		}

		sort.Strings(names)

		return "", tracoorArtifact{}, fmt.Errorf("unknown artifact type %q. Available: %v", artifact, names)
	}

	return artifact, spec, nil
}

// tracoorFilterPayload builds the filter portion of a Tracoor list/count
// request body from operation args, rejecting filters the artifact type
// does not declare.
func tracoorFilterPayload(args map[string]any, spec tracoorArtifact) (map[string]any, error) {
	payload := make(map[string]any, len(spec.stringFilters)+len(spec.numberFilters))

	for _, key := range spec.stringFilters {
		if value := optionalStringArg(args, key); value != "" {
			payload[key] = value
		}
	}

	for _, key := range spec.numberFilters {
		raw, ok := args[key]
		if !ok || raw == nil {
			continue
		}

		value, err := parseInt64Arg(raw, key)
		if err != nil {
			return nil, err
		}

		payload[key] = value
	}

	return payload, nil
}

// tracoorUniqueFields validates the "fields" arg against the artifact's
// declared unique-value fields.
func tracoorUniqueFields(args map[string]any, spec tracoorArtifact) ([]string, error) {
	raw := optionalSliceArg(args, "fields")
	if len(raw) == 0 {
		return nil, fmt.Errorf("fields is required (one or more of %v)", spec.uniqueFields)
	}

	valid := make(map[string]bool, len(spec.uniqueFields))
	for _, field := range spec.uniqueFields {
		valid[field] = true
	}

	fields := make([]string, 0, len(raw))

	for _, item := range raw {
		field, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("fields must be a list of strings")
		}

		if !valid[field] {
			return nil, fmt.Errorf("unknown field %q for this artifact type. Available: %v", field, spec.uniqueFields)
		}

		fields = append(fields, field)
	}

	return fields, nil
}

// tracoorParseCount extracts the count from a Tracoor count response, which
// encodes uint64 as a quoted string per proto3 JSON.
func tracoorParseCount(body []byte) (int64, error) {
	var payload struct {
		Count json.Number `json:"count"`
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		return 0, fmt.Errorf("invalid Tracoor count response: %w", err)
	}

	if payload.Count == "" {
		return 0, nil
	}

	count, err := strconv.ParseInt(payload.Count.String(), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid Tracoor count %q: %w", payload.Count, err)
	}

	return count, nil
}
