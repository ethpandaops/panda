package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/ethpandaops/panda/pkg/operations"
	"github.com/ethpandaops/panda/pkg/proxy"
)

// buildoorRequestTimeout bounds every upstream buildoor call.
const buildoorRequestTimeout = 30 * time.Second

// buildoorInstance is one buildoor builder instance behind a network's
// overview service. Name is the short instance identifier derived from the
// host (e.g. "lighthouse-geth-1"); URL is the instance API base URL.
type buildoorInstance struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

func (s *service) handleBuildoorOperation(operationID string, w http.ResponseWriter, r *http.Request) bool {
	switch operationID {
	case "buildoor.list_networks":
		s.handleBuildoorListNetworks(w)
	case "buildoor.list_instances":
		s.handleBuildoorListInstances(w, r)
	case "buildoor.get_overview":
		s.handleBuildoorInstanceGet(w, r, "/api/buildoor/overview", nil)
	case "buildoor.get_action_plan":
		s.handleBuildoorSlotRangeGet(w, r, "/api/buildoor/action-plan")
	case "buildoor.get_slot_results":
		s.handleBuildoorSlotRangeGet(w, r, "/api/buildoor/slot-results")
	case "buildoor.update_action_plan":
		s.handleBuildoorUpdateActionPlan(w, r)
	case "buildoor.test_transform":
		s.handleBuildoorTestTransform(w, r)
	default:
		return false
	}

	return true
}

func (s *service) handleBuildoorListNetworks(w http.ResponseWriter) {
	networks, err := s.buildoorNetworks()
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	items := make([]listItem, 0, len(networks))
	for name, baseURL := range networks {
		items = append(items, listItem{
			Name: name,
			URL:  baseURL,
			Type: "buildoor",
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

func (s *service) handleBuildoorListInstances(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	instances, status, err := s.buildoorInstances(r.Context(), req.Args)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	items := make([]listItem, 0, len(instances))
	for _, instance := range instances {
		items = append(items, listItem{
			Name: instance.Name,
			URL:  instance.URL,
			Type: "buildoor",
		})
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: map[string]any{"instances": items},
	})
}

// handleBuildoorInstanceGet proxies a GET on an instance API path.
func (s *service) handleBuildoorInstanceGet(
	w http.ResponseWriter,
	r *http.Request,
	path string,
	params url.Values,
) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	instance, status, err := s.resolveBuildoorInstance(r.Context(), req.Args)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	s.buildoorPassthrough(r.Context(), w, http.MethodGet, instance.URL, path, params, nil, "")
}

// handleBuildoorSlotRangeGet proxies a GET that takes the action-plan API's
// inclusive min_slot/max_slot range.
func (s *service) handleBuildoorSlotRangeGet(w http.ResponseWriter, r *http.Request, path string) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	instance, status, err := s.resolveBuildoorInstance(r.Context(), req.Args)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	params := url.Values{}

	for _, key := range []string{"min_slot", "max_slot"} {
		value, err := parseInt64Arg(req.Args[key], key)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}

		params.Set(key, fmt.Sprintf("%d", value))
	}

	s.buildoorPassthrough(r.Context(), w, http.MethodGet, instance.URL, path, params, nil, "")
}

// handleBuildoorUpdateActionPlan forwards a bulk action-plan mutation to an
// instance. The updates array is passed through verbatim — buildoor owns the
// PlanUpdate schema and validates it (bad jq / unknown fields → 400, frozen
// or past slots → 409).
//
// Credentials, in precedence order: an explicit caller token (auth_token arg)
// goes direct to the instance and keeps per-user attribution in buildoor's
// audit log; otherwise the mutation routes through a proxy that advertises
// buildoor — the proxy is the credential boundary and mints the devnet
// authenticatoor JWT itself (the acting human stays attributed via the proxy's
// audit log). With neither, the direct call reaches buildoor unauthenticated
// and its 401 comes back with the remedy.
func (s *service) handleBuildoorUpdateActionPlan(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	instance, status, err := s.resolveBuildoorInstance(r.Context(), req.Args)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	updates := optionalSliceArg(req.Args, "updates")
	if len(updates) == 0 {
		writeAPIError(w, http.StatusBadRequest, "updates is required")
		return
	}

	body, err := json.Marshal(map[string]any{"updates": updates})
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, fmt.Sprintf("encoding updates: %v", err))
		return
	}

	token := optionalStringArg(req.Args, "auth_token")

	if token == "" {
		if route, ok := s.buildoorRoute(); ok {
			network := optionalStringArg(req.Args, "network")
			path := fmt.Sprintf("/buildoor/%s/%s/api/buildoor/action-plan",
				url.PathEscape(network), url.PathEscape(instance.Name))

			data, proxyStatus, responseHeaders, err := s.proxyRequestWithService(
				r.Context(), route, http.MethodPost, path,
				bytes.NewReader(body), http.Header{"Content-Type": []string{"application/json"}},
			)
			if err != nil {
				writeAPIError(w, http.StatusBadGateway, fmt.Sprintf("proxy request failed: %v", err))
				return
			}

			if proxyStatus < 200 || proxyStatus >= 300 {
				writeAPIError(w, proxyStatus, buildoorErrorMessage(proxyStatus, data))
				return
			}

			writePassthroughResponse(w, http.StatusOK, responseHeaders.Get("Content-Type"), data)

			return
		}
	}

	s.buildoorPassthrough(
		r.Context(), w, http.MethodPost, instance.URL, "/api/buildoor/action-plan", nil, body, token,
	)
}

// buildoorRoute resolves the proxy route that advertises credentialed buildoor
// access, mirroring workflowRoute.
func (s *service) buildoorRoute() (proxy.Service, bool) {
	if s.proxyService == nil {
		return nil, false
	}

	if router, ok := s.proxyService.(proxy.Router); ok {
		client, found := router.BuildoorRoute()
		if !found {
			return nil, false
		}

		return client, true
	}

	if provider, ok := s.proxyService.(proxy.BuildoorInfoProvider); ok && provider.BuildoorAvailable() {
		return s.proxyService, true
	}

	return nil, false
}

// handleBuildoorTestTransform evaluates a jq expression against a sample
// builder object on the instance (a captured artifact when sample_slot is
// given and available, otherwise a template). Read-only; no auth required.
func (s *service) handleBuildoorTestTransform(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	instance, status, err := s.resolveBuildoorInstance(r.Context(), req.Args)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	target, err := requiredStringArg(req.Args, "target")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	expression, err := requiredStringArg(req.Args, "expression")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	payload := map[string]any{"target": target, "expression": expression}
	if sampleSlot := optionalIntArg(req.Args, "sample_slot", 0); sampleSlot > 0 {
		payload["sample_slot"] = sampleSlot
	}

	body, err := json.Marshal(payload)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, fmt.Sprintf("encoding request: %v", err))
		return
	}

	s.buildoorPassthrough(
		r.Context(), w, http.MethodPost, instance.URL, "/api/buildoor/action-plan/test-transform", nil, body, "",
	)
}

func (s *service) buildoorNetworks() (map[string]string, error) {
	if s.cartographoorClient == nil {
		return nil, fmt.Errorf("buildoor is unavailable")
	}

	networks := make(map[string]string)

	for name, network := range s.cartographoorClient.GetActiveNetworks() {
		if network.ServiceURLs != nil && network.ServiceURLs.Buildoor != "" {
			networks[name] = network.ServiceURLs.Buildoor
		}
	}

	return networks, nil
}

func (s *service) buildoorOverviewURL(args map[string]any) (string, int, error) {
	network, err := requiredStringArg(args, "network")
	if err != nil {
		return "", http.StatusBadRequest, err
	}

	networks, err := s.buildoorNetworks()
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

		return "", http.StatusNotFound, fmt.Errorf("no buildoor on network %q. Available: %v", network, names)
	}

	return baseURL, http.StatusOK, nil
}

// buildoorInstances resolves the network's per-instance API URLs from the
// overview service's host list.
func (s *service) buildoorInstances(ctx context.Context, args map[string]any) ([]buildoorInstance, int, error) {
	overviewURL, status, err := s.buildoorOverviewURL(args)
	if err != nil {
		return nil, status, err
	}

	requestCtx, cancel := context.WithTimeout(ctx, buildoorRequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodGet,
		strings.TrimRight(overviewURL, "/")+"/api/overview/hosts",
		nil,
	)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("creating buildoor request: %w", err)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("querying buildoor overview: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("reading buildoor overview response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, http.StatusBadGateway,
			fmt.Errorf("buildoor overview returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload struct {
		Hosts []struct {
			URL   string `json:"url"`
			Label string `json:"label"`
		} `json:"hosts"`
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("invalid buildoor overview response: %w", err)
	}

	instances := make([]buildoorInstance, 0, len(payload.Hosts))
	for _, host := range payload.Hosts {
		instances = append(instances, buildoorInstance{
			Name: buildoorInstanceName(host.Label, host.URL),
			URL:  strings.TrimRight(host.URL, "/"),
		})
	}

	return instances, http.StatusOK, nil
}

// resolveBuildoorInstance resolves the "instance" arg (short name, full label,
// or URL) against the network's instance list.
func (s *service) resolveBuildoorInstance(
	ctx context.Context, args map[string]any,
) (buildoorInstance, int, error) {
	instance, err := requiredStringArg(args, "instance")
	if err != nil {
		return buildoorInstance{}, http.StatusBadRequest, err
	}

	instances, status, err := s.buildoorInstances(ctx, args)
	if err != nil {
		return buildoorInstance{}, status, err
	}

	names := make([]string, 0, len(instances))

	for _, candidate := range instances {
		if instance == candidate.Name || strings.TrimRight(instance, "/") == candidate.URL {
			return candidate, http.StatusOK, nil
		}

		names = append(names, candidate.Name)
	}

	return buildoorInstance{}, http.StatusNotFound,
		fmt.Errorf("unknown buildoor instance %q. Available: %v", instance, names)
}

// buildoorInstanceName derives the short instance identifier from an overview
// host entry: the first DNS label of the host, minus the "api-"/"buildoor-"
// deployment prefixes (e.g. "api-buildoor-lighthouse-geth-1.srv.…" →
// "lighthouse-geth-1"). IP-addressed hosts stay whole (host:port) — a
// first-label cut would collapse them all to the first octet.
func buildoorInstanceName(label, rawURL string) string {
	host := label
	if host == "" {
		if parsed, err := url.Parse(rawURL); err == nil {
			host = parsed.Host
		}
	}

	hostname := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		hostname = h
	}

	if net.ParseIP(hostname) != nil {
		return host
	}

	name, _, _ := strings.Cut(host, ".")
	name = strings.TrimPrefix(name, "api-")
	name = strings.TrimPrefix(name, "buildoor-")

	if name == "" {
		return host
	}

	return name
}

// buildoorPassthrough executes one instance API request and forwards the
// upstream response verbatim — including non-2xx statuses, so buildoor's own
// validation errors (bad jq → 400, frozen slot → 409, bad token → 401) reach
// the caller with their original message.
func (s *service) buildoorPassthrough(
	ctx context.Context,
	w http.ResponseWriter,
	method, instanceURL, path string,
	params url.Values,
	body []byte,
	bearerToken string,
) {
	requestURL := instanceURL + path
	if len(params) > 0 {
		requestURL += "?" + params.Encode()
	}

	requestCtx, cancel := context.WithTimeout(ctx, buildoorRequestTimeout)
	defer cancel()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(requestCtx, method, requestURL, reader)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, fmt.Sprintf("creating buildoor request: %v", err))
		return
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, fmt.Sprintf("executing buildoor request: %v", err))
		return
	}
	defer func() { _ = resp.Body.Close() }()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, fmt.Sprintf("reading buildoor response: %v", err))
		return
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeAPIError(w, resp.StatusCode, buildoorErrorMessage(resp.StatusCode, responseBody))
		return
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}

	writePassthroughResponse(w, http.StatusOK, contentType, responseBody)
}

// buildoorErrorMessage extracts buildoor's {"error": …} message and adds the
// operator hint for the two statuses with a known remedy.
func buildoorErrorMessage(status int, body []byte) string {
	message := strings.TrimSpace(string(body))

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err == nil {
		if text, ok := payload["error"].(string); ok && text != "" {
			message = text
		}
	}

	switch status {
	case http.StatusUnauthorized:
		return message + " (buildoor mutations need either a proxy that advertises buildoor" +
			" — hosted panda-proxy with the buildoor credential configured —" +
			" or a personal authenticatoor bearer token via --token)"
	case http.StatusConflict:
		return message + " (the slot is past or its plan is frozen — plans freeze ~1 slot ahead, target slots ≥2 ahead)"
	default:
		return message
	}
}
