package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// buildoorTokenSkew refreshes a cached authenticatoor JWT this long before
// its expiry so an in-flight request never carries an expiring token.
const buildoorTokenSkew = 60 * time.Second

// BuildoorConfig holds devnet buildoor API access configuration. The proxy is
// the credential boundary: it authenticates to each devnet's authenticatoor
// (auth.<network>.<suffix>, behind Cloudflare Access) with a CF Access service
// token — the authenticatoor mints a JWT whose subject is the service token's
// common name — and injects that JWT as the bearer on buildoor instance API
// calls. StaticToken bypasses minting for local/ad-hoc deployments.
type BuildoorConfig struct {
	CFAccessClientID     string
	CFAccessClientSecret string
	StaticToken          string
	// DomainSuffix is the devnet DNS zone. Default: "ethpandaops.io".
	DomainSuffix string
}

// resolvedSuffix returns the configured domain suffix or the default.
func (c BuildoorConfig) resolvedSuffix() string {
	if s := strings.TrimSpace(c.DomainSuffix); s != "" {
		return s
	}

	return "ethpandaops.io"
}

// buildoorToken is one cached per-network authenticatoor JWT.
type buildoorToken struct {
	token   string
	expires time.Time
}

// buildoorBearerKey carries the resolved bearer from ServeHTTP (where minting
// can fail cleanly) into the reverse proxy's Rewrite (which cannot).
type buildoorBearerKey struct{}

// BuildoorHandler proxies requests to devnet buildoor instance APIs.
// Path format: /{network}/{instance}/api/... (mounted under /buildoor), with
// the upstream host constructed as api-buildoor-{instance}.srv.{network}.{suffix}
// — the deterministic per-instance ingress the devnet deployments publish.
type BuildoorHandler struct {
	log        logrus.FieldLogger
	cfg        BuildoorConfig
	httpClient *http.Client

	mu     sync.Mutex
	tokens map[string]buildoorToken

	proxyMu sync.RWMutex
	proxies map[string]*httputil.ReverseProxy
}

// NewBuildoorHandler creates a new buildoor handler.
func NewBuildoorHandler(log logrus.FieldLogger, cfg BuildoorConfig) *BuildoorHandler {
	return &BuildoorHandler{
		log:        log.WithField("handler", "buildoor"),
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 15 * time.Second},
		tokens:     make(map[string]buildoorToken, 4),
		proxies:    make(map[string]*httputil.ReverseProxy, 8),
	}
}

// ServeHTTP handles buildoor instance API requests. The subtree mount does not
// strip the route prefix, so the handler drops it itself (mirroring ethnode).
func (h *BuildoorHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/buildoor")

	parts := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 3)
	if len(parts) < 3 || parts[2] == "" {
		http.Error(w, "invalid path: must be /{network}/{instance}/api/...", http.StatusBadRequest)
		return
	}

	network, instance := parts[0], parts[1]
	rest := "/" + parts[2]

	if !validSegment.MatchString(network) {
		http.Error(w, "invalid network name: must match [a-z0-9-]", http.StatusBadRequest)
		return
	}

	if !validSegment.MatchString(instance) {
		http.Error(w, "invalid instance name: must match [a-z0-9-]", http.StatusBadRequest)
		return
	}

	// Only the buildoor HTTP API is exposed — never the metrics/debug routes.
	if !strings.HasPrefix(rest, "/api/") {
		http.Error(w, "invalid path: only /api/ endpoints are proxied", http.StatusBadRequest)
		return
	}

	bearer, err := h.bearerForNetwork(r.Context(), network)
	if err != nil {
		h.log.WithError(err).WithField("network", network).Warn("Buildoor token acquisition failed")
		http.Error(w, fmt.Sprintf("acquiring devnet auth token: %v", err), http.StatusBadGateway)

		return
	}

	host := fmt.Sprintf("api-buildoor-%s.srv.%s.%s", instance, network, h.cfg.resolvedSuffix())
	proxy := h.getOrCreateProxy(host)

	r.URL.Path = rest

	h.log.WithFields(logrus.Fields{
		"network":  network,
		"instance": instance,
		"path":     rest,
		"method":   r.Method,
		"upstream": host,
	}).Debug("Proxying buildoor request")

	proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), buildoorBearerKey{}, bearer)))
}

// bearerForNetwork returns the bearer for the network's buildoor instances:
// the static token when configured, else a cached-or-minted authenticatoor JWT.
func (h *BuildoorHandler) bearerForNetwork(ctx context.Context, network string) (string, error) {
	if h.cfg.StaticToken != "" {
		return h.cfg.StaticToken, nil
	}

	if h.cfg.CFAccessClientID == "" || h.cfg.CFAccessClientSecret == "" {
		return "", fmt.Errorf("no buildoor credential configured (cf_access_client_id/secret or static_token)")
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if cached, ok := h.tokens[network]; ok && time.Now().Before(cached.expires.Add(-buildoorTokenSkew)) {
		return cached.token, nil
	}

	minted, err := h.mintToken(ctx, network)
	if err != nil {
		return "", err
	}

	h.tokens[network] = minted

	return minted.token, nil
}

// mintToken exchanges the CF Access service token for a devnet authenticatoor
// JWT via GET https://auth.{network}.{suffix}/auth/token. The authenticatoor
// accepts CF Access service tokens (allowServiceTokens) and uses the token's
// common name as the JWT subject — that identity appears in buildoor's audit
// log; the acting human stays attributed in this proxy's own audit log.
func (h *BuildoorHandler) mintToken(ctx context.Context, network string) (buildoorToken, error) {
	tokenURL := fmt.Sprintf("https://auth.%s.%s/auth/token", network, h.cfg.resolvedSuffix())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL, nil)
	if err != nil {
		return buildoorToken{}, fmt.Errorf("creating token request: %w", err)
	}

	req.Header.Set("CF-Access-Client-Id", h.cfg.CFAccessClientID)
	req.Header.Set("CF-Access-Client-Secret", h.cfg.CFAccessClientSecret)

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return buildoorToken{}, fmt.Errorf("requesting token from %s: %w", tokenURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return buildoorToken{}, fmt.Errorf("reading token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return buildoorToken{}, fmt.Errorf(
			"authenticatoor at %s returned HTTP %d (is the CF Access service token included in the auth app policy?)",
			tokenURL, resp.StatusCode,
		)
	}

	var payload struct {
		Token string `json:"token"`
		Expr  int64  `json:"expr"`
	}

	if err := json.Unmarshal(body, &payload); err != nil || payload.Token == "" {
		return buildoorToken{}, fmt.Errorf("invalid token response from %s", tokenURL)
	}

	expires := time.Unix(payload.Expr, 0)
	if payload.Expr == 0 {
		expires = time.Now().Add(5 * time.Minute)
	}

	return buildoorToken{token: payload.Token, expires: expires}, nil
}

// getOrCreateProxy returns a cached reverse proxy for the host.
func (h *BuildoorHandler) getOrCreateProxy(host string) *httputil.ReverseProxy {
	h.proxyMu.RLock()
	proxy, ok := h.proxies[host]
	h.proxyMu.RUnlock()

	if ok {
		return proxy
	}

	h.proxyMu.Lock()
	defer h.proxyMu.Unlock()

	if proxy, ok = h.proxies[host]; ok {
		return proxy
	}

	targetURL := &url.URL{Scheme: "https", Host: host}

	rp := &httputil.ReverseProxy{Transport: newProxyTransport(false)}
	rp.Rewrite = func(pr *httputil.ProxyRequest) {
		pr.SetURL(targetURL)
		pr.SetXForwarded()
		pr.Out.Host = host

		// The inbound Authorization is the caller's proxy bearer; replace it
		// with the devnet token resolved in ServeHTTP.
		pr.Out.Header.Del("Authorization")

		if bearer, _ := pr.In.Context().Value(buildoorBearerKey{}).(string); bearer != "" {
			pr.Out.Header.Set("Authorization", "Bearer "+bearer)
		}
	}

	h.proxies[host] = rp

	return rp
}
