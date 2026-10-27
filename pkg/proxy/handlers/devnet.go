package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

const (
	// devnetTokenSkew refreshes a cached authenticatoor JWT this long before
	// its expiry so an in-flight request never carries an expiring token.
	devnetTokenSkew = 60 * time.Second

	// defaultDevnetDomainSuffix is the DNS zone devnet services live under.
	defaultDevnetDomainSuffix = "ethpandaops.io"
)

// DevnetServiceNames are the devnet services the relay can front. Each runs
// at https://{service}.{label}.{suffix} and authorizes writes with a bearer
// JWT minted by the devnet's authenticatoor.
var DevnetServiceNames = []string{"assertoor", "spamoor"}

// DevnetConfig holds devnet service API access configuration. The proxy is
// the credential boundary: it trades a Cloudflare Access service token for a
// per-devnet authenticatoor JWT at https://auth.{label}.{suffix}/auth/token
// and injects that JWT as the bearer on service API calls. StaticToken
// bypasses minting for local/dev deployments.
type DevnetConfig struct {
	Services             []string
	CFAccessClientID     string
	CFAccessClientSecret string
	StaticToken          string
	DomainSuffix         string
}

// ResolvedSuffix returns the configured domain suffix or the default.
func (c DevnetConfig) ResolvedSuffix() string {
	if s := strings.TrimSpace(c.DomainSuffix); s != "" {
		return s
	}

	return defaultDevnetDomainSuffix
}

type devnetToken struct {
	token   string
	expires time.Time
}

// devnetBearerKey carries the bearer resolved in ServeHTTP (where minting can
// fail cleanly) into the reverse proxy's Rewrite (which cannot).
type devnetBearerKey struct{}

// DevnetHandler proxies requests to devnet service APIs.
// Path format: /devnet/{service}/{label}/api/... where label is the devnet's
// DNS label (e.g. glamsterdam-devnet-8). The upstream is always
// https://{service}.{label}.{suffix}: callers pick a configured service and a
// single DNS label, never a host.
type DevnetHandler struct {
	log        logrus.FieldLogger
	cfg        DevnetConfig
	services   map[string]struct{}
	transport  http.RoundTripper
	mintClient *http.Client

	mu     sync.Mutex
	tokens map[string]devnetToken

	proxyMu sync.RWMutex
	proxies map[string]*httputil.ReverseProxy
}

// NewDevnetHandler creates a new devnet service handler.
func NewDevnetHandler(log logrus.FieldLogger, cfg DevnetConfig) *DevnetHandler {
	return newDevnetHandler(log, cfg, newProxyTransport(false))
}

func newDevnetHandler(log logrus.FieldLogger, cfg DevnetConfig, transport http.RoundTripper) *DevnetHandler {
	services := make(map[string]struct{}, len(cfg.Services))
	for _, name := range cfg.Services {
		services[name] = struct{}{}
	}

	return &DevnetHandler{
		log:       log.WithField("handler", "devnet"),
		cfg:       cfg,
		services:  services,
		transport: transport,
		mintClient: &http.Client{
			Transport: transport,
			Timeout:   15 * time.Second,
			// Without a service-token policy CF Access answers with a login
			// redirect; following it would turn a policy gap into an opaque
			// HTML-instead-of-JSON error.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		tokens:  make(map[string]devnetToken, 4),
		proxies: make(map[string]*httputil.ReverseProxy, 8),
	}
}

// ParseDevnetPath splits /devnet/{service}/{label}/{rest} into its parts. ok
// is false when the path has fewer than three segments after the mount.
func ParseDevnetPath(path string) (service, label, rest string, ok bool) {
	parts := strings.SplitN(strings.TrimPrefix(strings.TrimPrefix(path, "/devnet"), "/"), "/", 3)
	if len(parts) < 3 {
		return "", "", "", false
	}

	return parts[0], parts[1], "/" + parts[2], true
}

// ServeHTTP handles devnet service API requests.
func (h *DevnetHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	service, label, rest, ok := ParseDevnetPath(r.URL.Path)
	if !ok {
		http.Error(w, "invalid path: must be /devnet/{service}/{label}/api/...", http.StatusBadRequest)

		return
	}

	if _, configured := h.services[service]; !configured {
		http.Error(w, fmt.Sprintf("devnet service %q is not configured on this proxy", service), http.StatusNotFound)

		return
	}

	if !validSegment.MatchString(label) {
		http.Error(w, "invalid devnet label: must match [a-z0-9-]", http.StatusBadRequest)

		return
	}

	// Only the service HTTP API is exposed — never the web UI or metrics. A
	// non-canonical path (dot segments, //) could normalize upstream to
	// something outside /api/.
	if !strings.HasPrefix(rest, "/api/") || strings.TrimSuffix(rest, "/") != path.Clean(rest) {
		http.Error(w, "invalid path: only /api/ endpoints are proxied", http.StatusBadRequest)

		return
	}

	bearer, err := h.bearerFor(r.Context(), label)
	if err != nil {
		h.log.WithError(err).WithField("label", label).Warn("Devnet token acquisition failed")
		http.Error(w, fmt.Sprintf("acquiring devnet auth token: %v", err), http.StatusBadGateway)

		return
	}

	host := fmt.Sprintf("%s.%s.%s", service, label, h.cfg.ResolvedSuffix())
	r.URL.Path = rest
	r.URL.RawPath = ""

	h.log.WithFields(logrus.Fields{
		"service":  service,
		"label":    label,
		"path":     rest,
		"method":   r.Method,
		"upstream": host,
	}).Debug("Proxying devnet service request")

	h.proxyFor(host, label).ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), devnetBearerKey{}, bearer)))
}

// bearerFor returns the static token when configured, else a cached-or-minted
// authenticatoor JWT for the devnet label.
func (h *DevnetHandler) bearerFor(ctx context.Context, label string) (string, error) {
	if h.cfg.StaticToken != "" {
		return h.cfg.StaticToken, nil
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if cached, ok := h.tokens[label]; ok && time.Now().Before(cached.expires.Add(-devnetTokenSkew)) {
		return cached.token, nil
	}

	minted, err := h.mintToken(ctx, label)
	if err != nil {
		return "", err
	}

	h.tokens[label] = minted

	return minted.token, nil
}

// forgetToken drops the cached JWT for label so the next request re-mints,
// covering a devnet whose signing key rotated under a still-unexpired token.
func (h *DevnetHandler) forgetToken(label string) {
	h.mu.Lock()
	delete(h.tokens, label)
	h.mu.Unlock()
}

// mintToken exchanges the CF Access service token for an authenticatoor JWT
// via GET https://auth.{label}.{suffix}/auth/token. The JWT subject is the
// service token's common name, so the service's own audit log shows the
// proxy; the acting human stays attributed in the proxy's audit log.
func (h *DevnetHandler) mintToken(ctx context.Context, label string) (devnetToken, error) {
	tokenURL := fmt.Sprintf("https://auth.%s.%s/auth/token", label, h.cfg.ResolvedSuffix())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL, nil)
	if err != nil {
		return devnetToken{}, fmt.Errorf("creating token request: %w", err)
	}

	req.Header.Set("CF-Access-Client-Id", h.cfg.CFAccessClientID)
	req.Header.Set("CF-Access-Client-Secret", h.cfg.CFAccessClientSecret)

	resp, err := h.mintClient.Do(req)
	if err != nil {
		return devnetToken{}, fmt.Errorf("requesting token from %s: %w", tokenURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return devnetToken{}, fmt.Errorf("reading token response: %w", err)
	}

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return devnetToken{}, fmt.Errorf(
			"authenticatoor at %s redirected to login: its CF Access application has no service-token policy, or the service token is invalid",
			tokenURL,
		)
	}

	if resp.StatusCode != http.StatusOK {
		return devnetToken{}, fmt.Errorf("authenticatoor at %s returned HTTP %d", tokenURL, resp.StatusCode)
	}

	var payload struct {
		Token string `json:"token"`
		Expr  int64  `json:"expr"`
	}

	if err := json.Unmarshal(body, &payload); err != nil || payload.Token == "" {
		return devnetToken{}, fmt.Errorf("invalid token response from %s", tokenURL)
	}

	if payload.Expr == 0 {
		return devnetToken{}, fmt.Errorf("token response from %s has no expiry", tokenURL)
	}

	return devnetToken{token: payload.Token, expires: time.Unix(payload.Expr, 0)}, nil
}

// proxyFor returns a cached reverse proxy for the upstream host.
func (h *DevnetHandler) proxyFor(host, label string) *httputil.ReverseProxy {
	h.proxyMu.RLock()
	rp, ok := h.proxies[host]
	h.proxyMu.RUnlock()

	if ok {
		return rp
	}

	h.proxyMu.Lock()
	defer h.proxyMu.Unlock()

	if rp, ok = h.proxies[host]; ok {
		return rp
	}

	target := &url.URL{Scheme: "https", Host: host}

	rp = &httputil.ReverseProxy{Transport: h.transport}
	rp.Rewrite = func(pr *httputil.ProxyRequest) {
		pr.SetURL(target)
		pr.SetXForwarded()

		// The inbound Authorization is the caller's proxy bearer and cookies
		// could carry a browser session; neither may reach the service.
		pr.Out.Header.Del("Authorization")
		pr.Out.Header.Del("Cookie")

		if bearer, _ := pr.In.Context().Value(devnetBearerKey{}).(string); bearer != "" {
			pr.Out.Header.Set("Authorization", "Bearer "+bearer)
		}

		// Services bind tokens to the request host, so Host must be the
		// upstream's own name.
		pr.Out.Host = host
	}

	rp.ModifyResponse = func(resp *http.Response) error {
		if resp.StatusCode == http.StatusUnauthorized && h.cfg.StaticToken == "" {
			h.forgetToken(label)
		}

		return nil
	}

	rp.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		h.log.WithError(err).WithField("upstream", host).Error("Proxy error")
		http.Error(w, fmt.Sprintf("proxy error: %v", err), http.StatusBadGateway)
	}

	h.proxies[host] = rp

	return rp
}
