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
	"strings"
	"time"

	"github.com/ethpandaops/panda/pkg/operations"
)

// defaultForkyListLimit caps metadata listings unless the caller asks for more.
const defaultForkyListLimit = 100

func (s *service) handleForkyOperation(operationID string, w http.ResponseWriter, r *http.Request) bool {
	switch operationID {
	case "forky.list_networks":
		s.handleForkyListNetworks(w)
	case "forky.get_base_url":
		s.handleForkyBaseURL(w, r)
	case "forky.get_now":
		s.handleForkyDataGet(w, r, "/api/v1/ethereum/now")
	case "forky.get_spec":
		s.handleForkyDataGet(w, r, "/api/v1/ethereum/spec")
	case "forky.get_frame":
		s.handleForkyGetFrame(w, r)
	case "forky.list_frames":
		s.handleForkyMetadataList(w, r, "/api/v1/metadata", "frames")
	case "forky.list_nodes":
		s.handleForkyMetadataList(w, r, "/api/v1/metadata/nodes", "nodes")
	case "forky.list_slots":
		s.handleForkyMetadataList(w, r, "/api/v1/metadata/slots", "slots")
	case "forky.list_epochs":
		s.handleForkyMetadataList(w, r, "/api/v1/metadata/epochs", "epochs")
	case "forky.list_labels":
		s.handleForkyMetadataList(w, r, "/api/v1/metadata/labels", "labels")
	case "forky.link_frame":
		s.handleForkyLink(w, r, "frame_id", "/snapshot/%s")
	case "forky.link_node":
		s.handleForkyLink(w, r, "node", "/node/%s")
	default:
		return false
	}

	return true
}

func (s *service) handleForkyListNetworks(w http.ResponseWriter) {
	networks, err := s.forkyNetworks()
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	items := make([]listItem, 0, len(networks))
	for name, baseURL := range networks {
		items = append(items, listItem{
			Name: name,
			URL:  baseURL,
			Type: "forky",
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

func (s *service) handleForkyBaseURL(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	baseURL, status, err := s.forkyBaseURL(req.Args)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: map[string]any{"base_url": baseURL},
	})
}

// handleForkyDataGet serves operations that map to a Forky GET endpoint whose
// response is a {"data": {...}} envelope, returning the unwrapped object.
func (s *service) handleForkyDataGet(w http.ResponseWriter, r *http.Request, path string) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	baseURL, status, err := s.forkyBaseURL(req.Args)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	body, _, status, err := s.forkyAPIRequest(r.Context(), http.MethodGet, baseURL, path, nil)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	data, err := forkyDataObject(body)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, err.Error())
		return
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: data,
		Meta: map[string]any{"network": optionalStringArg(req.Args, "network")},
	})
}

func (s *service) handleForkyGetFrame(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	baseURL, status, err := s.forkyBaseURL(req.Args)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	frameID, err := requiredStringArg(req.Args, "frame_id")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	body, contentType, status, err := s.forkyAPIRequest(
		r.Context(),
		http.MethodGet,
		baseURL,
		"/api/v1/frames/"+url.PathEscape(frameID),
		nil,
	)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	writePassthroughResponse(w, http.StatusOK, contentType, body)
}

// handleForkyMetadataList serves the POST /api/v1/metadata* family. The Forky
// response nests the page under {"data": {<itemsKey>: [...], "pagination":
// {"total": N}}}; it is flattened to {<itemsKey>: [...], "total": N}.
func (s *service) handleForkyMetadataList(w http.ResponseWriter, r *http.Request, path, itemsKey string) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	baseURL, status, err := s.forkyBaseURL(req.Args)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	query, err := forkyMetadataQuery(req.Args)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	body, _, status, err := s.forkyAPIRequest(r.Context(), http.MethodPost, baseURL, path, query)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	data, err := forkyDataObject(body)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, err.Error())
		return
	}

	result := map[string]any{itemsKey: data[itemsKey]}
	if pagination, ok := data["pagination"].(map[string]any); ok {
		result["total"] = pagination["total"]
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: result,
		Meta: map[string]any{"network": optionalStringArg(req.Args, "network")},
	})
}

func (s *service) handleForkyLink(w http.ResponseWriter, r *http.Request, argName, pathTemplate string) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	baseURL, status, err := s.forkyBaseURL(req.Args)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	identifier, err := requiredStringArg(req.Args, argName)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: map[string]any{"url": strings.TrimRight(baseURL, "/") + fmt.Sprintf(pathTemplate, forkyEscapePath(identifier))},
		Meta: map[string]any{"network": optionalStringArg(req.Args, "network")},
	})
}

// forkyEscapePath escapes a UI path identifier per segment, keeping the "/"
// separators that node names contain — the Forky SPA routes node views with a
// splat path, matching how its own frontend links them.
func forkyEscapePath(identifier string) string {
	segments := strings.Split(identifier, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}

	return strings.Join(segments, "/")
}

func (s *service) forkyNetworks() (map[string]string, error) {
	if s.cartographoorClient == nil {
		return nil, fmt.Errorf("forky is unavailable")
	}

	networks := make(map[string]string)
	for name, network := range s.cartographoorClient.GetActiveNetworks() {
		if network.ServiceURLs != nil && network.ServiceURLs.Forky != "" {
			networks[name] = network.ServiceURLs.Forky
		}
	}

	return networks, nil
}

func (s *service) forkyBaseURL(args map[string]any) (string, int, error) {
	network, err := requiredStringArg(args, "network")
	if err != nil {
		return "", http.StatusBadRequest, err
	}

	networks, err := s.forkyNetworks()
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

func (s *service) forkyAPIRequest(
	ctx context.Context,
	method, baseURL, path string,
	payload any,
) ([]byte, string, int, error) {
	requestURL := strings.TrimRight(baseURL, "/") + path

	var requestBody io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, "", http.StatusInternalServerError, fmt.Errorf("encoding Forky request: %w", err)
		}

		requestBody = bytes.NewReader(encoded)
	}

	requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, method, requestURL, requestBody)
	if err != nil {
		return nil, "", http.StatusInternalServerError, fmt.Errorf("creating Forky request: %w", err)
	}

	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, "", http.StatusBadGateway, fmt.Errorf("executing Forky request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", http.StatusBadGateway, fmt.Errorf("reading Forky response: %w", err)
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

// forkyMetadataQuery builds a Forky MetadataQuery body ({filter, pagination})
// from operation args.
func forkyMetadataQuery(args map[string]any) (map[string]any, error) {
	filter := make(map[string]any, 8)

	for _, key := range []string{"node", "consensus_client", "event_source", "before", "after"} {
		if value := optionalStringArg(args, key); value != "" {
			filter[key] = value
		}
	}

	for _, key := range []string{"slot", "epoch"} {
		raw, ok := args[key]
		if !ok || raw == nil {
			continue
		}

		value, err := parseInt64Arg(raw, key)
		if err != nil {
			return nil, err
		}

		filter[key] = value
	}

	if labels := optionalSliceArg(args, "labels"); len(labels) > 0 {
		parsed := make([]string, 0, len(labels))
		for _, label := range labels {
			value, ok := label.(string)
			if !ok {
				return nil, fmt.Errorf("labels must be a list of strings")
			}

			parsed = append(parsed, value)
		}

		filter["labels"] = parsed
	}

	return map[string]any{
		"filter": filter,
		"pagination": map[string]any{
			"offset": optionalIntArg(args, "offset", 0),
			"limit":  optionalIntArg(args, "limit", defaultForkyListLimit),
		},
	}, nil
}

func forkyDataObject(body []byte) (map[string]any, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("invalid Forky JSON response: %w", err)
	}

	data, ok := payload["data"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("invalid Forky response: data is not an object")
	}

	return data, nil
}
