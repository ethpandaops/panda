package handlers

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"

	"github.com/sirupsen/logrus"
)

// FaucetConfig holds the basic-auth credential for the per-network agent PoW
// faucet. The faucet ingress is credential-gated (not open), and only the proxy
// holds this pair — so panda users can reach the faucet solely through the
// proxy, which authenticates them first.
type FaucetConfig struct {
	Username string
	Password string
}

// FaucetHandler reverse-proxies agent PoW faucet requests to the network's
// faucet host, attaching the basic-auth credential. Path: /faucet/{network}/...
// It mirrors EthNodeHandler but has no per-instance segment (one faucet/network).
type FaucetHandler struct {
	log    logrus.FieldLogger
	cfg    FaucetConfig
	mu     sync.RWMutex
	proxes map[string]*httputil.ReverseProxy
}

// NewFaucetHandler creates a new faucet handler.
func NewFaucetHandler(log logrus.FieldLogger, cfg FaucetConfig) *FaucetHandler {
	return &FaucetHandler{
		log:    log.WithField("handler", "faucet"),
		cfg:    cfg,
		proxes: make(map[string]*httputil.ReverseProxy, 8),
	}
}

// ServeHTTP handles /faucet/{network}/{rest...}, forwarding {rest} (and the
// query string) to the network's faucet.
func (h *FaucetHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/faucet/")
	if path == r.URL.Path {
		http.Error(w, "invalid path: must start with /faucet/", http.StatusBadRequest)

		return
	}

	parts := strings.SplitN(path, "/", 2)
	network := parts[0]

	rest := "/"
	if len(parts) == 2 && parts[1] != "" {
		rest = "/" + parts[1]
	}

	if !validSegment.MatchString(network) {
		http.Error(w, "invalid network name: must match [a-z0-9-]", http.StatusBadRequest)

		return
	}

	host := faucetUpstreamHost(network)
	proxy := h.getOrCreateProxy(host)

	r.URL.Path = rest

	h.log.WithFields(logrus.Fields{
		"network":  network,
		"path":     rest,
		"method":   r.Method,
		"upstream": host,
	}).Debug("Proxying faucet request")

	proxy.ServeHTTP(w, r)
}

// faucetUpstreamHost returns the credential-gated faucet host for a network.
func faucetUpstreamHost(network string) string {
	return fmt.Sprintf("faucet-agents.%s.ethpandaops.io", network)
}

// getOrCreateProxy returns a cached reverse proxy for the host, creating one if needed.
func (h *FaucetHandler) getOrCreateProxy(host string) *httputil.ReverseProxy {
	h.mu.RLock()
	proxy, ok := h.proxes[host]
	h.mu.RUnlock()

	if ok {
		return proxy
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if proxy, ok = h.proxes[host]; ok {
		return proxy
	}

	targetURL := &url.URL{
		Scheme: "https",
		Host:   host,
	}

	rp := &httputil.ReverseProxy{Transport: newProxyTransport(false)}

	cfg := h.cfg
	rp.Rewrite = func(pr *httputil.ProxyRequest) {
		pr.SetURL(targetURL)
		pr.SetXForwarded()

		// Drop the caller's bearer; the upstream faucet uses basic auth.
		pr.Out.Header.Del("Authorization")

		if cfg.Username != "" {
			pr.Out.SetBasicAuth(cfg.Username, cfg.Password)
		}

		pr.Out.Host = pr.Out.URL.Host
		pr.Out.Header.Del("Host")
	}

	rp.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		h.log.WithError(err).WithField("upstream", host).Error("Proxy error")
		http.Error(w, fmt.Sprintf("proxy error: %v", err), http.StatusBadGateway)
	}

	h.proxes[host] = rp

	return rp
}
