package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/ethpandaops/cartographoor/pkg/discovery"

	"github.com/ethpandaops/panda/pkg/operations"
	"github.com/ethpandaops/panda/pkg/proxy/handlers"
)

// devnetMethods are the HTTP methods devnet.request relays.
var devnetMethods = map[string]struct{}{
	http.MethodGet: {}, http.MethodPost: {}, http.MethodPut: {}, http.MethodPatch: {}, http.MethodDelete: {},
}

func (s *service) handleDevnetOperation(operationID string, w http.ResponseWriter, r *http.Request) bool {
	switch operationID {
	case "devnet.list_networks":
		s.handleDevnetListNetworks(w)
	case "devnet.request":
		s.handleDevnetRequest(w, r)
	default:
		return false
	}

	return true
}

// devnetServiceURL returns the cartographoor URL of service on a network.
func devnetServiceURL(network discovery.Network, service string) string {
	if network.ServiceURLs == nil {
		return ""
	}

	switch service {
	case "spamoor":
		return network.ServiceURLs.Spamoor
	case "assertoor":
		return network.ServiceURLs.Assertoor
	default:
		return ""
	}
}

// relayedDevnetServices maps each devnet service the proxy relays for this
// caller to its advertised domain suffix.
func (s *service) relayedDevnetServices() map[string]string {
	if s.proxyService == nil {
		return nil
	}

	relayed := make(map[string]string, len(handlers.DevnetServiceNames))
	for _, info := range s.proxyService.DevnetDatasourceInfo() {
		relayed[info.Name] = info.Metadata["domain_suffix"]
	}

	return relayed
}

func (s *service) handleDevnetListNetworks(w http.ResponseWriter) {
	if s.cartographoorClient == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "devnet network discovery is unavailable")
		return
	}

	relayed := s.relayedDevnetServices()

	items := make([]listItem, 0)
	for name, network := range s.cartographoorClient.GetActiveNetworks() {
		services := make(map[string]any, len(handlers.DevnetServiceNames))
		for _, service := range handlers.DevnetServiceNames {
			if serviceURL := devnetServiceURL(network, service); serviceURL != "" {
				_, writable := relayed[service]
				services[service] = map[string]any{"url": serviceURL, "relayed": writable}
			}
		}

		if len(services) == 0 {
			continue
		}

		items = append(items, listItem{Name: name, Type: "devnet", Extra: map[string]any{"services": services}})
	}

	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: map[string]any{"networks": items},
	})
}

// devnetLabel returns the DNS label of a service URL, requiring the exact
// host shape {service}.{label}.{suffix} the proxy will rebuild. Cartographoor
// network names differ from labels (focil-devnet-2 is served from
// heze-devnet-2), so the label always comes from the URL.
func devnetLabel(serviceURL, service, suffix string) (string, error) {
	parsed, err := url.Parse(serviceURL)
	if err != nil {
		return "", fmt.Errorf("invalid %s URL %q: %w", service, serviceURL, err)
	}

	if parsed.Scheme != "https" || (parsed.Path != "" && parsed.Path != "/") || parsed.Port() != "" {
		return "", fmt.Errorf("%s URL %q is not a bare https://%s.<devnet>.%s origin", service, serviceURL, service, suffix)
	}

	label := strings.TrimSuffix(strings.TrimPrefix(parsed.Hostname(), service+"."), "."+suffix)
	if parsed.Hostname() != fmt.Sprintf("%s.%s.%s", service, label, suffix) || !ethnodeSegmentPattern.MatchString(label) {
		return "", fmt.Errorf("%s URL %q is not of the form https://%s.<devnet>.%s", service, serviceURL, service, suffix)
	}

	return label, nil
}

// devnetAPIPath validates a caller-supplied service API path.
func devnetAPIPath(apiPath string) (string, error) {
	if !strings.HasPrefix(apiPath, "/api/") || strings.ContainsAny(apiPath, "?#") ||
		strings.TrimSuffix(apiPath, "/") != path.Clean(apiPath) {
		return "", fmt.Errorf("path must be a canonical /api/... path without a query string (pass query params via query); got %q", apiPath)
	}

	// The relay buffers responses, so a server-sent-event stream would hang
	// until the request times out.
	if strings.HasSuffix(apiPath, "/stream") || strings.HasSuffix(apiPath, "/events") || strings.Contains(apiPath, "/events/") {
		return "", fmt.Errorf("streaming endpoint %q is not supported through the relay; poll the non-streaming endpoint instead", apiPath)
	}

	return apiPath, nil
}

type devnetRequest struct {
	network, service, method, path string
	query                          url.Values
	body                           []byte
}

func parseDevnetRequest(args map[string]any) (devnetRequest, error) {
	var req devnetRequest

	var err error
	if req.network, err = requiredStringArg(args, "network"); err != nil {
		return req, err
	}

	if req.service, err = requiredStringArg(args, "service"); err != nil {
		return req, err
	}

	req.method = strings.ToUpper(optionalStringArg(args, "method"))
	if req.method == "" {
		req.method = http.MethodGet
	}

	if _, ok := devnetMethods[req.method]; !ok {
		return req, fmt.Errorf("unsupported method %q: use GET, POST, PUT, PATCH, or DELETE", req.method)
	}

	rawPath, err := requiredStringArg(args, "path")
	if err != nil {
		return req, err
	}

	if req.path, err = devnetAPIPath(rawPath); err != nil {
		return req, err
	}

	req.query = url.Values{}
	for key, value := range optionalMapArg(args, "query") {
		req.query.Set(key, fmt.Sprint(value))
	}

	if body, ok := args["body"]; ok && body != nil {
		if req.body, err = json.Marshal(body); err != nil {
			return req, fmt.Errorf("encoding body: %w", err)
		}
	}

	return req, nil
}

func (s *service) handleDevnetRequest(w http.ResponseWriter, r *http.Request) {
	opReq, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	req, err := parseDevnetRequest(opReq.Args)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	suffix, relayed := s.relayedDevnetServices()[req.service]
	if !relayed {
		writeAPIError(w, http.StatusForbidden, fmt.Sprintf(
			"devnet service %q is not available through the proxy: it is not configured under devnet_services, or your account is not in its allowed_orgs. "+
				"Read-only endpoints are public at the service URL from devnet.list_networks", req.service))

		return
	}

	if s.cartographoorClient == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "devnet network discovery is unavailable")
		return
	}

	network, ok := s.cartographoorClient.GetNetwork(req.network)
	if !ok {
		writeAPIError(w, http.StatusNotFound, fmt.Sprintf("unknown network %q; list devnets with devnet.list_networks", req.network))
		return
	}

	serviceURL := devnetServiceURL(network, req.service)
	if serviceURL == "" {
		writeAPIError(w, http.StatusNotFound, fmt.Sprintf("network %q has no %s service", req.network, req.service))
		return
	}

	label, err := devnetLabel(serviceURL, req.service, suffix)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, err.Error())
		return
	}

	requestPath := fmt.Sprintf("/devnet/%s/%s%s", req.service, label, req.path)
	if len(req.query) > 0 {
		requestPath += "?" + req.query.Encode()
	}

	headers := http.Header{handlers.DatasourceHeader: []string{req.service}}

	var body io.Reader
	if req.body != nil {
		body = bytes.NewReader(req.body)
		headers.Set("Content-Type", "application/json")
	}

	data, status, respHeaders, err := s.proxyDatasourceRequest(r.Context(), "devnet", req.service, req.method, requestPath, body, headers)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	if status < 200 || status >= 300 {
		writeAPIError(w, status, devnetErrorMessage(req.service, status, data))
		return
	}

	writePassthroughResponse(w, status, respHeaders.Get("Content-Type"), data)
}

// devnetErrorMessage adds the remedy for the relay's own failure modes to an
// upstream error body.
func devnetErrorMessage(service string, status int, body []byte) string {
	message := strings.TrimSpace(string(body))

	switch {
	case status == http.StatusForbidden && strings.HasPrefix(message, "forbidden: insufficient org membership"):
		return fmt.Sprintf("%s: your account is not in the allowed_orgs for the %s relay", message, service)
	case status == http.StatusUnauthorized:
		return fmt.Sprintf("%s rejected the relay's token (HTTP 401: %s); the proxy's devnet_services credential "+
			"is invalid for this devnet or its authenticatoor does not accept it", service, message)
	default:
		return message
	}
}
