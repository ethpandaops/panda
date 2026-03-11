package transport

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"github.com/sirupsen/logrus"
)

// validSegment matches only lowercase alphanumeric characters and hyphens.
var validSegment = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$|^[a-z0-9]$`)

// EthNodeConfig holds credentials for Ethereum node API access.
// A single credential pair is used for all bn-*.srv.*.ethpandaops.io
// and rpc-*.srv.*.ethpandaops.io endpoints.
type EthNodeConfig struct {
	Username string
	Password string
}

// EthNodeHandler proxies requests to Ethereum beacon and execution nodes.
// Unlike other handlers that use static reverse proxies per instance,
// this handler constructs upstream URLs dynamically from path segments.
type EthNodeHandler struct {
	log    logrus.FieldLogger
	cfg    EthNodeConfig
	mu     sync.RWMutex
	proxes map[string]*httputil.ReverseProxy
}

// NewEthNodeHandler creates a new Ethereum node handler.
func NewEthNodeHandler(log logrus.FieldLogger, cfg EthNodeConfig) *EthNodeHandler {
	return &EthNodeHandler{
		log:    log.WithField("handler", "ethnode"),
		cfg:    cfg,
		proxes: make(map[string]*httputil.ReverseProxy, 16),
	}
}

// ServeHTTP handles beacon and execution node requests.
// Path format: /{beacon|execution}/{network}/{instance}/...
// The first segment (beacon/execution) determines the upstream host pattern.
func (h *EthNodeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Parse path: /{mode}/{network}/{instance}/{rest...}
	// Mode is already stripped by the mux routing, so we need to determine
	// mode from the original registered path prefix.
	path := r.URL.Path

	// Determine mode from path prefix.
	var mode string

	switch {
	case strings.HasPrefix(path, "/beacon/"):
		mode = "beacon"
		path = strings.TrimPrefix(path, "/beacon/")
	case strings.HasPrefix(path, "/execution/"):
		mode = "execution"
		path = strings.TrimPrefix(path, "/execution/")
	default:
		http.Error(w, "invalid path: must start with /beacon/ or /execution/", http.StatusBadRequest)

		return
	}

	// Split remaining path into network/instance/rest.
	parts := strings.SplitN(path, "/", 3)
	if len(parts) < 2 {
		http.Error(w, "invalid path: must include /{network}/{instance}/...", http.StatusBadRequest)

		return
	}

	network := parts[0]
	instance := parts[1]

	rest := "/"
	if len(parts) == 3 && parts[2] != "" {
		rest = "/" + parts[2]
	}

	// Validate network and instance segments.
	if !validSegment.MatchString(network) {
		http.Error(w, "invalid network name: must match [a-z0-9-]", http.StatusBadRequest)

		return
	}

	if !validSegment.MatchString(instance) {
		http.Error(w, "invalid instance name: must match [a-z0-9-]", http.StatusBadRequest)

		return
	}

	// Construct upstream host.
	var host string

	switch mode {
	case "beacon":
		host = fmt.Sprintf("bn-%s.srv.%s.ethpandaops.io", instance, network)
	case "execution":
		host = fmt.Sprintf("rpc-%s.srv.%s.ethpandaops.io", instance, network)
	}

	// Get or create reverse proxy for this host.
	proxy := h.getOrCreateProxy(host)

	// Rewrite the request path.
	r.URL.Path = rest

	h.log.WithFields(logrus.Fields{
		"mode":     mode,
		"network":  network,
		"instance": instance,
		"path":     rest,
		"method":   r.Method,
		"upstream": host,
	}).Debug("Proxying ethnode request")

	proxy.ServeHTTP(w, r)
}

// getOrCreateProxy returns a cached reverse proxy for the host, creating one if needed.
func (h *EthNodeHandler) getOrCreateProxy(host string) *httputil.ReverseProxy {
	h.mu.RLock()
	proxy, ok := h.proxes[host]
	h.mu.RUnlock()

	if ok {
		return proxy
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	// Double-check after acquiring write lock.
	if proxy, ok = h.proxes[host]; ok {
		return proxy
	}

	targetURL := &url.URL{
		Scheme: "https",
		Host:   host,
	}

	rp := httputil.NewSingleHostReverseProxy(targetURL)

	rp.Transport = newProxyTransport(false)

	cfg := h.cfg
	originalDirector := rp.Director
	rp.Director = func(req *http.Request) {
		originalDirector(req)

		// Remove the sandbox's Authorization header (Bearer token).
		req.Header.Del("Authorization")

		// Add basic auth credentials.
		if cfg.Username != "" {
			req.SetBasicAuth(cfg.Username, cfg.Password)
		}

		// Set req.Host to the target host for correct Host header.
		req.Host = req.URL.Host
		req.Header.Del("Host")
	}

	rp.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		h.log.WithError(err).WithField("upstream", host).Error("Proxy error")
		http.Error(w, fmt.Sprintf("proxy error: %v", err), http.StatusBadGateway)
	}

	h.proxes[host] = rp

	return rp
}
