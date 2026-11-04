package handlers

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testTokenURL   = "https://idp.example/application/o/token/"
	coreCaller     = "core-caller-token"
	outsiderCaller = "outsider-caller-token"
)

// devnetUpstream stands in for the IdP token endpoint, every devnet
// authenticatoor and every service: the handler's transport is pointed at
// it, so URLs, Host headers and headers are exactly what would go on the wire.
type devnetUpstream struct {
	mu sync.Mutex

	exchanges     []url.Values
	exchangeError int // non-zero: the IdP answers this status

	mints      []*http.Request
	mintStatus map[string]int // auth host -> status override

	seen     []*http.Request
	upstream int
	planted  http.Header // response headers the service sends back
}

func (u *devnetUpstream) RoundTrip(req *http.Request) (*http.Response, error) {
	u.mu.Lock()
	defer u.mu.Unlock()

	respond := func(status int, body string) *http.Response {
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}
	}

	switch {
	case req.URL.String() == testTokenURL:
		body, _ := io.ReadAll(req.Body)
		form, _ := url.ParseQuery(string(body))
		u.exchanges = append(u.exchanges, form)

		if u.exchangeError != 0 {
			return respond(u.exchangeError, `upstream failure`), nil
		}

		if form.Get("client_assertion") != coreCaller {
			return respond(http.StatusBadRequest, `{"error":"invalid_grant"}`), nil
		}

		return respond(http.StatusOK, fmt.Sprintf(`{"access_token":"devnet-at-%d","expires_in":300}`, len(u.exchanges))), nil

	case strings.HasPrefix(req.URL.Host, "auth."):
		u.mints = append(u.mints, req.Clone(req.Context()))

		if status := u.mintStatus[req.URL.Host]; status != 0 {
			return respond(status, ``), nil
		}

		if !strings.HasPrefix(req.Header.Get("Authorization"), "Bearer devnet-at-") {
			return respond(http.StatusUnauthorized, ``), nil
		}

		return respond(http.StatusOK, fmt.Sprintf(`{"token":"jwt-%d","user":"alice@example.com","expr":%d,"now":0}`,
			len(u.mints), time.Now().Add(30*time.Minute).Unix())), nil
	}

	u.seen = append(u.seen, req.Clone(req.Context()))

	status := http.StatusOK
	if u.upstream != 0 {
		status = u.upstream
	}

	resp := respond(status, `{"ok":true}`)
	for k, v := range u.planted {
		resp.Header[k] = v
	}

	return resp, nil
}

func (u *devnetUpstream) counts() (exchanges, mints, seen int) {
	u.mu.Lock()
	defer u.mu.Unlock()

	return len(u.exchanges), len(u.mints), len(u.seen)
}

func testIdentity() *DevnetIdentityConfig {
	return &DevnetIdentityConfig{TokenURL: testTokenURL, ClientID: "devnet-authenticatoor"}
}

func newTestDevnetHandler(cfg DevnetConfig, upstream http.RoundTripper) *DevnetHandler {
	if cfg.Services == nil {
		cfg.Services = []string{"spamoor", "assertoor"}
	}

	return newDevnetHandler(logrus.New(), cfg, upstream)
}

func serveDevnetAs(h http.Handler, caller, method, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	if caller != "" {
		req.Header.Set("Authorization", "Bearer "+caller)
	}

	req.Header.Set("Cookie", "session=browser")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	return rec
}

func serveDevnet(h http.Handler, method, target string) *httptest.ResponseRecorder {
	return serveDevnetAs(h, coreCaller, method, target)
}

func TestDevnetHandlerRejectsPaths(t *testing.T) {
	upstream := &devnetUpstream{}
	h := newTestDevnetHandler(DevnetConfig{Services: []string{"spamoor"}, Identity: testIdentity()}, upstream)

	tests := []struct {
		name   string
		path   string
		status int
	}{
		{"too short", "/devnet/spamoor/glamsterdam-devnet-8", http.StatusBadRequest},
		{"unconfigured service", "/devnet/assertoor/glamsterdam-devnet-8/api/v1/tests", http.StatusNotFound},
		{"unknown service", "/devnet/dora/glamsterdam-devnet-8/api/v1/epochs", http.StatusNotFound},
		{"dotted label", "/devnet/spamoor/evil.example.com/api/spammers", http.StatusBadRequest},
		{"uppercase label", "/devnet/spamoor/Devnet/api/spammers", http.StatusBadRequest},
		{"web ui path", "/devnet/spamoor/glamsterdam-devnet-8/wallets", http.StatusBadRequest},
		{"metrics path", "/devnet/spamoor/glamsterdam-devnet-8/metrics", http.StatusBadRequest},
		{"dot segments", "/devnet/spamoor/glamsterdam-devnet-8/api/../metrics", http.StatusBadRequest},
		{"double slash", "/devnet/spamoor/glamsterdam-devnet-8/api//spammers", http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serveDevnet(h, http.MethodGet, tt.path)
			assert.Equal(t, tt.status, rec.Code, rec.Body.String())
		})
	}

	exchanges, mints, seen := upstream.counts()
	assert.Zero(t, exchanges+mints, "rejected paths must not acquire tokens")
	assert.Zero(t, seen, "rejected paths must not reach a service")
}

func TestDevnetHandlerActsAsTheCaller(t *testing.T) {
	upstream := &devnetUpstream{}
	h := newTestDevnetHandler(DevnetConfig{Identity: testIdentity()}, upstream)

	for range 2 {
		rec := serveDevnet(h, http.MethodPost, "/devnet/spamoor/heze-devnet-2/api/spammer/7/pause?x=1")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, "alice@example.com", rec.Header().Get(DevnetIdentityHeader))
		assert.Empty(t, rec.Header().Get(DevnetIdentityReasonHeader))
	}

	require.Len(t, upstream.exchanges, 1, "the exchanged token is cached per caller")
	ex := upstream.exchanges[0]
	assert.Equal(t, "client_credentials", ex.Get("grant_type"))
	assert.Equal(t, "devnet-authenticatoor", ex.Get("client_id"))
	assert.Equal(t, "urn:ietf:params:oauth:client-assertion-type:jwt-bearer", ex.Get("client_assertion_type"))
	assert.Equal(t, coreCaller, ex.Get("client_assertion"))
	assert.Equal(t, "openid email profile groups", ex.Get("scope"))

	require.Len(t, upstream.mints, 1, "the devnet token is cached per caller and devnet")
	mint := upstream.mints[0]
	assert.Equal(t, "https://auth.heze-devnet-2.ethpandaops.io/authentik/token", mint.URL.String())
	assert.Equal(t, "Bearer devnet-at-1", mint.Header.Get("Authorization"), "devnets only see the exchanged token")

	require.Len(t, upstream.seen, 2)
	out := upstream.seen[0]
	assert.Equal(t, "https", out.URL.Scheme)
	assert.Equal(t, "spamoor.heze-devnet-2.ethpandaops.io", out.URL.Host)
	assert.Equal(t, "spamoor.heze-devnet-2.ethpandaops.io", out.Host, "services bind tokens to Host")
	assert.Equal(t, "/api/spammer/7/pause", out.URL.Path)
	assert.Equal(t, "x=1", out.URL.RawQuery)
	assert.Equal(t, http.MethodPost, out.Method)
	assert.Equal(t, "Bearer jwt-1", out.Header.Get("Authorization"), "caller bearer is replaced")
	assert.Empty(t, out.Header.Get("Cookie"))

	// A second devnet reuses the exchanged token but mints its own JWT.
	rec := serveDevnet(h, http.MethodGet, "/devnet/assertoor/glamsterdam-devnet-8/api/v1/tests")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Len(t, upstream.exchanges, 1)
	assert.Len(t, upstream.mints, 2)
	assert.Equal(t, "assertoor.glamsterdam-devnet-8.ethpandaops.io", upstream.seen[2].Host)
}

func TestDevnetHandlerNeverSharesTokensAcrossCallers(t *testing.T) {
	upstream := &devnetUpstream{}
	h := newTestDevnetHandler(DevnetConfig{Identity: testIdentity()}, upstream)

	require.Equal(t, http.StatusOK, serveDevnet(h, http.MethodPost, "/devnet/spamoor/dev-1/api/spammer/1/start").Code)

	rec := serveDevnetAs(h, outsiderCaller, http.MethodPost, "/devnet/spamoor/dev-1/api/spammer/1/start")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "anonymous", rec.Header().Get(DevnetIdentityHeader))
	assert.Empty(t, upstream.seen[1].Header.Get("Authorization"), "an outsider never rides on a member's cached token")
}

func TestDevnetHandlerRefusedExchangeRelaysAnonymously(t *testing.T) {
	upstream := &devnetUpstream{}
	h := newTestDevnetHandler(DevnetConfig{Identity: testIdentity()}, upstream)

	for range 2 {
		rec := serveDevnetAs(h, outsiderCaller, http.MethodGet, "/devnet/spamoor/dev-1/api/spammers")
		require.Equal(t, http.StatusOK, rec.Code, "public reads still work")
		assert.Equal(t, "anonymous", rec.Header().Get(DevnetIdentityHeader))
		assert.Contains(t, rec.Header().Get(DevnetIdentityReasonHeader), "refused the devnet token exchange (invalid_grant)")
	}

	exchanges, mints, seen := upstream.counts()
	assert.Equal(t, 1, exchanges, "a refusal is cached")
	assert.Zero(t, mints)
	require.Equal(t, 2, seen)
	assert.Empty(t, upstream.seen[0].Header.Get("Authorization"))
}

func TestDevnetHandlerDevnetWithoutBearerProviderRelaysAnonymously(t *testing.T) {
	upstream := &devnetUpstream{mintStatus: map[string]int{"auth.old-devnet.ethpandaops.io": http.StatusNotFound}}
	h := newTestDevnetHandler(DevnetConfig{Identity: testIdentity()}, upstream)

	rec := serveDevnet(h, http.MethodGet, "/devnet/spamoor/old-devnet/api/spammers")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "anonymous", rec.Header().Get(DevnetIdentityHeader))
	assert.Contains(t, rec.Header().Get(DevnetIdentityReasonHeader), "predates bearer support")
	assert.Empty(t, upstream.seen[0].Header.Get("Authorization"))
}

func TestDevnetHandlerInfrastructureFailuresFail(t *testing.T) {
	t.Run("idp error", func(t *testing.T) {
		upstream := &devnetUpstream{exchangeError: http.StatusBadGateway}
		h := newTestDevnetHandler(DevnetConfig{Identity: testIdentity()}, upstream)

		rec := serveDevnet(h, http.MethodGet, "/devnet/spamoor/dev-1/api/spammers")
		assert.Equal(t, http.StatusBadGateway, rec.Code)
		assert.Contains(t, rec.Body.String(), "token exchange")
		assert.Empty(t, upstream.seen, "no silent downgrade to anonymous")
	})

	t.Run("idp rate limited is not a refusal", func(t *testing.T) {
		upstream := &devnetUpstream{exchangeError: http.StatusTooManyRequests}
		h := newTestDevnetHandler(DevnetConfig{Identity: testIdentity()}, upstream)

		rec := serveDevnet(h, http.MethodGet, "/devnet/spamoor/dev-1/api/spammers")
		assert.Equal(t, http.StatusBadGateway, rec.Code)
		assert.Contains(t, rec.Body.String(), "rate limited")
		assert.Empty(t, upstream.seen)
	})

	t.Run("authenticatoor error", func(t *testing.T) {
		upstream := &devnetUpstream{mintStatus: map[string]int{"auth.dev-1.ethpandaops.io": http.StatusInternalServerError}}
		h := newTestDevnetHandler(DevnetConfig{Identity: testIdentity()}, upstream)

		rec := serveDevnet(h, http.MethodGet, "/devnet/spamoor/dev-1/api/spammers")
		assert.Equal(t, http.StatusBadGateway, rec.Code)
		assert.Empty(t, upstream.seen)
	})
}

func TestDevnetHandlerAnonymousWithoutIdentity(t *testing.T) {
	tests := []struct {
		name   string
		cfg    DevnetConfig
		caller string
		reason string
	}{
		{"no identity configured", DevnetConfig{}, coreCaller, "no devnet identity exchange configured"},
		{"no caller token", DevnetConfig{Identity: testIdentity()}, "", "carried no caller token"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &devnetUpstream{}
			h := newTestDevnetHandler(tt.cfg, upstream)

			rec := serveDevnetAs(h, tt.caller, http.MethodGet, "/devnet/spamoor/dev-1/api/spammers")
			require.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, "anonymous", rec.Header().Get(DevnetIdentityHeader))
			assert.Contains(t, rec.Header().Get(DevnetIdentityReasonHeader), tt.reason)
			assert.Empty(t, upstream.exchanges)
			assert.Empty(t, upstream.seen[0].Header.Get("Authorization"))
		})
	}
}

func TestDevnetHandlerUpstreamCannotPlantIdentityHeaders(t *testing.T) {
	upstream := &devnetUpstream{planted: http.Header{
		DevnetIdentityHeader:       {"root@example.com"},
		DevnetIdentityReasonHeader: {"planted"},
	}}
	h := newTestDevnetHandler(DevnetConfig{Identity: testIdentity()}, upstream)

	rec := serveDevnet(h, http.MethodGet, "/devnet/spamoor/dev-1/api/spammers")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "alice@example.com", rec.Header().Get(DevnetIdentityHeader))
	assert.Empty(t, rec.Header().Get(DevnetIdentityReasonHeader))
}

func TestDevnetHandlerConfiguredProviderAndSuffix(t *testing.T) {
	upstream := &devnetUpstream{}
	id := testIdentity()
	id.AuthenticatoorProvider = "ethpandaops"
	id.Scopes = []string{"openid", "groups"}
	h := newTestDevnetHandler(DevnetConfig{DomainSuffix: "example.org", Identity: id}, upstream)

	rec := serveDevnet(h, http.MethodGet, "/devnet/spamoor/dev-1/api/spammers")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "openid groups", upstream.exchanges[0].Get("scope"))
	assert.Equal(t, "https://auth.dev-1.example.org/ethpandaops/token", upstream.mints[0].URL.String())
	assert.Equal(t, "spamoor.dev-1.example.org", upstream.seen[0].Host)
}

func TestDevnetHandlerUpstream401ForgetsToken(t *testing.T) {
	upstream := &devnetUpstream{upstream: http.StatusUnauthorized}
	h := newTestDevnetHandler(DevnetConfig{Identity: testIdentity()}, upstream)

	rec := serveDevnet(h, http.MethodPost, "/devnet/spamoor/dev-1/api/spammer/1/start")
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "upstream status passes through")

	upstream.upstream = 0
	rec = serveDevnet(h, http.MethodPost, "/devnet/spamoor/dev-1/api/spammer/1/start")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Len(t, upstream.mints, 2, "a rejected token is re-minted")
	assert.Len(t, upstream.exchanges, 1, "the exchanged token is still valid")
	assert.Equal(t, "Bearer jwt-2", upstream.seen[1].Header.Get("Authorization"))
}

func TestParseDevnetPath(t *testing.T) {
	service, label, rest, ok := ParseDevnetPath("/devnet/spamoor/dev-1/api/spammers")
	require.True(t, ok)
	assert.Equal(t, "spamoor", service)
	assert.Equal(t, "dev-1", label)
	assert.Equal(t, "/api/spammers", rest)

	_, _, _, ok = ParseDevnetPath("/devnet/spamoor")
	assert.False(t, ok)
}

// stallingMint blocks mints for one auth host until released; everything
// else answers immediately.
type stallingMint struct {
	devnetUpstream
	stallHost string
	release   chan struct{}
	stalled   chan struct{}
	stallOnce sync.Once
	stalls    int
	stallMu   sync.Mutex
}

func (s *stallingMint) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == s.stallHost {
		s.stallMu.Lock()
		s.stalls++
		s.stallMu.Unlock()
		s.stallOnce.Do(func() { close(s.stalled) })
		<-s.release
	}

	return s.devnetUpstream.RoundTrip(req)
}

func TestDevnetHandlerSlowMintDoesNotStallOtherDevnets(t *testing.T) {
	upstream := &stallingMint{
		stallHost: "auth.slow-devnet.ethpandaops.io",
		release:   make(chan struct{}),
		stalled:   make(chan struct{}),
	}
	h := newTestDevnetHandler(DevnetConfig{Services: []string{"spamoor"}, Identity: testIdentity()}, upstream)

	require.Equal(t, http.StatusOK, serveDevnet(h, http.MethodGet, "/devnet/spamoor/fast-devnet/api/spammers").Code)

	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.Equal(t, http.StatusOK, serveDevnet(h, http.MethodGet, "/devnet/spamoor/slow-devnet/api/spammers").Code)
		}()
	}

	<-upstream.stalled

	done := make(chan int, 1)
	go func() { done <- serveDevnet(h, http.MethodGet, "/devnet/spamoor/fast-devnet/api/spammers").Code }()

	select {
	case code := <-done:
		assert.Equal(t, http.StatusOK, code)
	case <-time.After(2 * time.Second):
		t.Fatal("a stalled mint for one devnet blocked a cached devnet")
	}

	close(upstream.release)
	wg.Wait()

	assert.Equal(t, 1, upstream.stalls, "concurrent requests for one devnet share a single mint")
}
