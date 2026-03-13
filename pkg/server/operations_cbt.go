package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/ethpandaops/panda/pkg/operations"
)

func (s *service) handleCBTOperation(operationID string, w http.ResponseWriter, r *http.Request) bool {
	switch operationID {
	case "cbt.list_networks":
		s.handleCBTListNetworks(w)
	case "cbt.list_models":
		s.handleCBTPassthrough(w, r, "/api/v1/models", "type", "database", "search")
	case "cbt.list_external_models":
		s.handleCBTPassthrough(w, r, "/api/v1/models/external", "database")
	case "cbt.get_external_model":
		s.handleCBTIDPassthrough(w, r, "/api/v1/models/external/%s")
	case "cbt.get_external_bounds":
		s.handleCBTOptionalIDPassthrough(w, r, "/api/v1/models/external/bounds", "/api/v1/models/external/%s/bounds")
	case "cbt.list_transformations":
		s.handleCBTPassthrough(w, r, "/api/v1/models/transformations", "database", "type", "status")
	case "cbt.get_transformation":
		s.handleCBTIDPassthrough(w, r, "/api/v1/models/transformations/%s")
	case "cbt.get_transformation_coverage":
		s.handleCBTOptionalIDPassthrough(w, r, "/api/v1/models/transformations/coverage", "/api/v1/models/transformations/%s/coverage")
	case "cbt.get_scheduled_runs":
		s.handleCBTOptionalIDPassthrough(w, r, "/api/v1/models/transformations/runs", "/api/v1/models/transformations/%s/runs")
	case "cbt.get_interval_types":
		s.handleCBTPassthrough(w, r, "/api/v1/interval/types")
	case "cbt.link_model":
		s.handleCBTLinkModel(w, r)
	default:
		return false
	}

	return true
}

func (s *service) handleCBTListNetworks(w http.ResponseWriter) {
	networks, err := s.cbtNetworks()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	items := make([]map[string]any, 0, len(networks))
	for name, baseURL := range networks {
		items = append(items, map[string]any{
			"name":    name,
			"cbt_url": baseURL,
		})
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i]["name"].(string) < items[j]["name"].(string)
	})

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: map[string]any{"networks": items},
	})
}

// handleCBTPassthrough forwards a GET request to the CBT API with optional query params.
func (s *service) handleCBTPassthrough(
	w http.ResponseWriter,
	r *http.Request,
	path string,
	optionalParams ...string,
) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	baseURL, status, err := s.cbtBaseURL(req.Args)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	params := url.Values{}
	for _, param := range optionalParams {
		if value := optionalStringArg(req.Args, param); value != "" {
			params.Set(param, value)
		}
	}

	body, contentType, status, err := s.cbtAPIGetRaw(r.Context(), baseURL, path, params)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	writePassthroughResponse(w, http.StatusOK, contentType, body)
}

// handleCBTIDPassthrough forwards a GET request with a required ID path parameter.
func (s *service) handleCBTIDPassthrough(
	w http.ResponseWriter,
	r *http.Request,
	pathTemplate string,
) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	baseURL, status, err := s.cbtBaseURL(req.Args)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	id, err := requiredStringArg(req.Args, "id")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	body, contentType, status, err := s.cbtAPIGetRaw(r.Context(), baseURL, fmt.Sprintf(pathTemplate, id), nil)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	writePassthroughResponse(w, http.StatusOK, contentType, body)
}

// handleCBTOptionalIDPassthrough forwards to allPath when no ID is given,
// or to idPathTemplate (with ID substituted) when an ID is provided.
func (s *service) handleCBTOptionalIDPassthrough(
	w http.ResponseWriter,
	r *http.Request,
	allPath, idPathTemplate string,
) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	baseURL, status, err := s.cbtBaseURL(req.Args)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	path := allPath
	params := url.Values{}

	if id := optionalStringArg(req.Args, "id"); id != "" {
		path = fmt.Sprintf(idPathTemplate, id)
	} else if database := optionalStringArg(req.Args, "database"); database != "" {
		params.Set("database", database)
	}

	body, contentType, status, err := s.cbtAPIGetRaw(r.Context(), baseURL, path, params)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	writePassthroughResponse(w, http.StatusOK, contentType, body)
}

func (s *service) handleCBTLinkModel(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	baseURL, status, err := s.cbtBaseURL(req.Args)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	id, err := requiredStringArg(req.Args, "id")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// CBT UI link: {base_url}/models/{database}/{table}
	// ID format is "database.table".
	parts := strings.SplitN(id, ".", 2)

	var linkPath string
	if len(parts) == 2 {
		linkPath = fmt.Sprintf("/models/%s/%s", parts[0], parts[1])
	} else {
		linkPath = fmt.Sprintf("/models/%s", id)
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: map[string]any{"url": strings.TrimRight(baseURL, "/") + linkPath},
		Meta: map[string]any{"network": optionalStringArg(req.Args, "network")},
	})
}

func (s *service) cbtNetworks() (map[string]string, error) {
	if s.cartographoorClient == nil {
		return nil, fmt.Errorf("cbt is unavailable")
	}

	networks := make(map[string]string)
	for name := range s.cartographoorClient.GetActiveNetworks() {
		networks[name] = fmt.Sprintf("https://cbt.%s.ethpandaops.io", name)
	}

	return networks, nil
}

func (s *service) cbtBaseURL(args map[string]any) (string, int, error) {
	network, err := requiredStringArg(args, "network")
	if err != nil {
		return "", http.StatusBadRequest, err
	}

	networks, err := s.cbtNetworks()
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

func (s *service) cbtAPIGetRaw(
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
		return nil, "", http.StatusInternalServerError, fmt.Errorf("creating CBT request: %w", err)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, "", http.StatusBadGateway, fmt.Errorf("executing CBT request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", http.StatusBadGateway, fmt.Errorf("reading CBT response: %w", err)
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
