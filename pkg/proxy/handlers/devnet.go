package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"golang.org/x/sync/singleflight"
)

const (
	// devnetTokenSkew retires a cached token this long before its expiry so
	// an in-flight request never carries an expiring token.
	devnetTokenSkew = 60 * time.Second

	// devnetRefusalTTL caches a refusal (caller not authorized, devnet
	// without a bearer provider) so anonymous traffic doesn't re-ask the
	// IdP and the authenticatoor on every request.
	devnetRefusalTTL = time.Minute

	// defaultDevnetDomainSuffix is the DNS zone devnet services live under.
	defaultDevnetDomainSuffix = "ethpandaops.io"

	// DefaultDevnetAuthenticatoorProvider names the authenticatoor bearer
	// provider instance: its machine token alias is /<name>/token.
	DefaultDevnetAuthenticatoorProvider = "authentik"

	// DevnetIdentityHeader reports on every relayed response who the
	// request ran as upstream: the authenticatoor user, or "anonymous".
	DevnetIdentityHeader = "X-Panda-Devnet-Identity"
	// DevnetIdentityReasonHeader explains an anonymous identity.
	DevnetIdentityReasonHeader = "X-Panda-Devnet-Identity-Reason"

	devnetAnonymous = "anonymous"

	clientAssertionTypeJWT = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"
)

// DevnetServiceNames are the devnet services the relay can front. Each runs
// at https://{service}.{label}.{suffix} and authorizes writes with a bearer
// JWT minted by the devnet's authenticatoor.
var DevnetServiceNames = []string{"assertoor", "spamoor"}

// DefaultDevnetIdentityScopes are requested in the token exchange so the
// devnet token carries the caller's email (audit identity) and groups.
var DefaultDevnetIdentityScopes = []string{"openid", "email", "profile", "groups"}

// oauthErrorCode bounds the IdP's error code before it is echoed in a header.
var oauthErrorCode = regexp.MustCompile(`^[a-z_]{1,64}$`)

// DevnetConfig holds devnet service API access configuration.
type DevnetConfig struct {
	Services     []string
	DomainSuffix string
	// Identity enables acting as the caller on the devnet. Nil relays every
	// request anonymously.
	Identity *DevnetIdentityConfig
}

// DevnetIdentityConfig turns the caller's IdP token into a devnet identity
// without the proxy holding any credential of its own:
//
//  1. Token exchange at the IdP (Authentik JWT federation): a
//     client_credentials grant for ClientID with the caller's token as
//     client_assertion. The IdP evaluates ClientID's access policy for the
//     caller and returns a short-lived token for that audience — the IdP is
//     the authority on who may act on devnets.
//  2. That token buys an authenticatoor JWT at
//     https://auth.{label}.{suffix}/{AuthenticatoorProvider}/token, the
//     devnet authenticatoor's bearer provider trusting ClientID's audience.
//
// Devnets therefore only ever see tokens scoped to devnet access, never the
// caller's proxy token.
type DevnetIdentityConfig struct {
	TokenURL               string
	ClientID               string
	Scopes                 []string
	AuthenticatoorProvider string
}

// ResolvedSuffix returns the configured domain suffix or the default.
func (c DevnetConfig) ResolvedSuffix() string {
	if s := strings.TrimSpace(c.DomainSuffix); s != "" {
		return s
	}

	return defaultDevnetDomainSuffix
}

// devnetCred is the cached outcome of acquiring a token: a token (with the
// identity it carries) or the reason the caller was refused. Either is
// reused until expires.
type devnetCred struct {
	token   string
	user    string
	refused string
	expires time.Time
}

// devnetCredCache caches devnetCreds per key, deduplicating concurrent
// acquisitions. Acquisitions run outside the lock and detached from the
// first caller's cancellation, since their result is shared.
type devnetCredCache struct {
	mu      sync.Mutex
	entries map[string]devnetCred
	flight  singleflight.Group
}

func (c *devnetCredCache) get(
	ctx context.Context, key string, acquire func(context.Context) (devnetCred, error),
) (devnetCred, error) {
	c.mu.Lock()
	cred, ok := c.entries[key]
	c.mu.Unlock()

	if ok && time.Now().Before(cred.expires) {
		return cred, nil
	}

	v, err, _ := c.flight.Do(key, func() (any, error) {
		cred, err := acquire(context.WithoutCancel(ctx))
		if err != nil {
			return devnetCred{}, err
		}

		now := time.Now()

		c.mu.Lock()
		defer c.mu.Unlock()

		if c.entries == nil {
			c.entries = make(map[string]devnetCred, 8)
		}

		for k, e := range c.entries {
			if now.After(e.expires) {
				delete(c.entries, k)
			}
		}

		c.entries[key] = cred

		return cred, nil
	})
	if err != nil {
		return devnetCred{}, err
	}

	return v.(devnetCred), nil //nolint:forcetypeassert // the Do closure only returns devnetCred
}

func (c *devnetCredCache) forget(key string) {
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
}

// devnetRequestKey carries the acquired credential from ServeHTTP (where
// acquisition can fail cleanly) into the reverse proxy's hooks.
type devnetRequestKey struct{}

type devnetRequest struct {
	cred    devnetCred
	mintKey string
}

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
	authClient *http.Client

	exchanged devnetCredCache // keyed by caller token
	minted    devnetCredCache // keyed by caller token + devnet label

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
		authClient: &http.Client{
			Transport: transport,
			Timeout:   15 * time.Second,
			// A redirect from a token endpoint means an edge login wall or a
			// misrouted URL; following it would turn that into an opaque
			// HTML-instead-of-JSON error.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
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

	req, err := h.identityFor(r.Context(), callerToken(r), label)
	if err != nil {
		h.log.WithError(err).WithField("label", label).Warn("Devnet identity acquisition failed")
		http.Error(w, fmt.Sprintf("acquiring devnet identity: %v", err), http.StatusBadGateway)

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
		"identity": req.identity(),
	}).Debug("Proxying devnet service request")

	h.proxyFor(host).ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), devnetRequestKey{}, req)))
}

// identityFor resolves the credential the request runs with upstream.
// Refusals (caller not authorized, devnet without a bearer provider) yield
// an anonymous request — the service then serves what it serves publicly —
// while infrastructure failures are returned as errors.
func (h *DevnetHandler) identityFor(ctx context.Context, caller, label string) (devnetRequest, error) {
	if h.cfg.Identity == nil {
		return devnetRequest{cred: devnetCred{refused: "this proxy has no devnet identity exchange configured"}}, nil
	}

	if caller == "" {
		return devnetRequest{cred: devnetCred{refused: "the request carried no caller token"}}, nil
	}

	callerKey := tokenKey(caller)

	exchanged, err := h.exchanged.get(ctx, callerKey, func(ctx context.Context) (devnetCred, error) {
		return h.exchange(ctx, caller)
	})
	if err != nil || exchanged.refused != "" {
		return devnetRequest{cred: exchanged}, err
	}

	mintKey := callerKey + "/" + label

	minted, err := h.minted.get(ctx, mintKey, func(ctx context.Context) (devnetCred, error) {
		return h.mint(ctx, exchanged.token, label)
	})

	return devnetRequest{cred: minted, mintKey: mintKey}, err
}

// exchange trades the caller's IdP token for a devnet-access token via a
// client_credentials grant with the caller's token as JWT client assertion.
func (h *DevnetHandler) exchange(ctx context.Context, caller string) (devnetCred, error) {
	id := h.cfg.Identity
	scopes := id.Scopes

	if len(scopes) == 0 {
		scopes = DefaultDevnetIdentityScopes
	}

	form := url.Values{
		"grant_type":            {"client_credentials"},
		"client_id":             {id.ClientID},
		"client_assertion_type": {clientAssertionTypeJWT},
		"client_assertion":      {caller},
		"scope":                 {strings.Join(scopes, " ")},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, id.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return devnetCred{}, fmt.Errorf("creating token exchange request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	status, body, err := h.do(req)
	if err != nil {
		return devnetCred{}, fmt.Errorf("token exchange at %s: %w", id.TokenURL, err)
	}

	var payload struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		Error       string `json:"error"`
	}

	_ = json.Unmarshal(body, &payload)

	switch {
	case status == http.StatusOK:
	case status == http.StatusTooManyRequests:
		return devnetCred{}, fmt.Errorf("token exchange at %s is rate limited (HTTP 429)", id.TokenURL)
	case status >= 400 && status < 500:
		code := payload.Error
		if !oauthErrorCode.MatchString(code) {
			code = "HTTP " + strconv.Itoa(status)
		}

		return refusal(fmt.Sprintf(
			"the IdP refused the devnet token exchange (%s): the caller is not authorized for devnet access, "+
				"or the caller's token cannot be exchanged (sign in again with an up-to-date panda)", code)), nil
	default:
		return devnetCred{}, fmt.Errorf("token exchange at %s returned HTTP %d", id.TokenURL, status)
	}

	if payload.AccessToken == "" || payload.ExpiresIn <= 0 {
		return devnetCred{}, fmt.Errorf("token exchange at %s returned no access token or expiry", id.TokenURL)
	}

	return devnetCred{
		token:   payload.AccessToken,
		expires: time.Now().Add(time.Duration(payload.ExpiresIn)*time.Second - devnetTokenSkew),
	}, nil
}

// mint buys an authenticatoor JWT for the devnet with the exchanged token.
func (h *DevnetHandler) mint(ctx context.Context, exchanged, label string) (devnetCred, error) {
	provider := h.cfg.Identity.AuthenticatoorProvider
	if provider == "" {
		provider = DefaultDevnetAuthenticatoorProvider
	}

	authHost := fmt.Sprintf("auth.%s.%s", label, h.cfg.ResolvedSuffix())
	tokenURL := fmt.Sprintf("https://%s/%s/token", authHost, provider)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL, nil)
	if err != nil {
		return devnetCred{}, fmt.Errorf("creating token request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+exchanged)

	status, body, err := h.do(req)
	if err != nil {
		return devnetCred{}, fmt.Errorf("requesting token from %s: %w", tokenURL, err)
	}

	switch {
	case status == http.StatusOK:
	case status == http.StatusNotFound:
		return refusal(fmt.Sprintf(
			"%s has no /%s/token endpoint: the devnet's authenticatoor predates bearer support or lacks the %q provider",
			authHost, provider, provider)), nil
	case status >= 300 && status < 400:
		return refusal(fmt.Sprintf("%s redirected /%s/token to a login page: edge protection covers the bearer endpoint",
			authHost, provider)), nil
	case status >= 400 && status < 500:
		return refusal(fmt.Sprintf("%s rejected the devnet identity (HTTP %d): its %q provider does not trust this IdP application",
			authHost, status, provider)), nil
	default:
		return devnetCred{}, fmt.Errorf("authenticatoor at %s returned HTTP %d", tokenURL, status)
	}

	var payload struct {
		Token string `json:"token"`
		User  string `json:"user"`
		Expr  int64  `json:"expr"`
	}

	if err := json.Unmarshal(body, &payload); err != nil || payload.Token == "" || payload.Expr == 0 {
		return devnetCred{}, fmt.Errorf("invalid token response from %s", tokenURL)
	}

	return devnetCred{
		token:   payload.Token,
		user:    payload.User,
		expires: time.Unix(payload.Expr, 0).Add(-devnetTokenSkew),
	}, nil
}

func (h *DevnetHandler) do(req *http.Request) (int, []byte, error) {
	resp, err := h.authClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, fmt.Errorf("reading response: %w", err)
	}

	return resp.StatusCode, body, nil
}

// proxyFor returns a cached reverse proxy for the upstream host.
func (h *DevnetHandler) proxyFor(host string) *httputil.ReverseProxy {
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

		if req, _ := pr.In.Context().Value(devnetRequestKey{}).(devnetRequest); req.cred.token != "" {
			pr.Out.Header.Set("Authorization", "Bearer "+req.cred.token)
		}

		// Services bind tokens to the request host, so Host must be the
		// upstream's own name.
		pr.Out.Host = host
	}

	rp.ModifyResponse = func(resp *http.Response) error {
		req, _ := resp.Request.Context().Value(devnetRequestKey{}).(devnetRequest)

		// Both headers are the relay's own: an upstream must not be able to
		// plant one.
		resp.Header.Set(DevnetIdentityHeader, req.identity())
		resp.Header.Del(DevnetIdentityReasonHeader)

		if req.cred.refused != "" {
			resp.Header.Set(DevnetIdentityReasonHeader, req.cred.refused)
		}

		// A rejected token is re-minted next time, covering a devnet whose
		// signing key rotated under a still-unexpired token.
		if resp.StatusCode == http.StatusUnauthorized && req.mintKey != "" {
			h.minted.forget(req.mintKey)
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

func (r devnetRequest) identity() string {
	if r.cred.token == "" {
		return devnetAnonymous
	}

	if r.cred.user == "" {
		return "unknown"
	}

	return r.cred.user
}

func refusal(reason string) devnetCred {
	return devnetCred{refused: reason, expires: time.Now().Add(devnetRefusalTTL)}
}

// callerToken returns the caller's bearer token, already verified by the
// proxy's authenticator.
func callerToken(r *http.Request) string {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}

	return strings.TrimSpace(token)
}

// tokenKey keys caches by a digest so raw caller tokens are not map keys.
func tokenKey(token string) string {
	sum := sha256.Sum256([]byte(token))

	return hex.EncodeToString(sum[:])
}
