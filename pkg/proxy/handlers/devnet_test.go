package handlers

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// devnetUpstream stands in for both authenticatoor and the services: the
// handler's transport is pointed at it, so the URL host, Host header and
// headers it sees are exactly what would go on the wire.
type devnetUpstream struct {
	mu         sync.Mutex
	mints      int
	mintHeader http.Header
	seen       []*http.Request
	mintStatus int
	upstream   int
}

func (u *devnetUpstream) RoundTrip(req *http.Request) (*http.Response, error) {
	u.mu.Lock()
	defer u.mu.Unlock()

	respond := func(status int, body string, header http.Header) *http.Response {
		if header == nil {
			header = http.Header{}
		}

		return &http.Response{
			StatusCode: status,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}
	}

	if strings.HasPrefix(req.URL.Host, "auth.") {
		u.mints++
		u.mintHeader = req.Header.Clone()

		if u.mintStatus != 0 {
			return respond(u.mintStatus, "", http.Header{"Location": []string{"https://team.cloudflareaccess.com/login"}}), nil
		}

		body := fmt.Sprintf(`{"token":"jwt-%d","user":"svc","expr":%d,"now":0}`, u.mints, time.Now().Add(time.Hour).Unix())

		return respond(http.StatusOK, body, nil), nil
	}

	u.seen = append(u.seen, req.Clone(req.Context()))

	status := http.StatusOK
	if u.upstream != 0 {
		status = u.upstream
	}

	return respond(status, `{"ok":true}`, nil), nil
}

func newTestDevnetHandler(cfg DevnetConfig, upstream *devnetUpstream) *DevnetHandler {
	if cfg.Services == nil {
		cfg.Services = []string{"spamoor", "assertoor"}
	}

	if cfg.StaticToken == "" {
		cfg.CFAccessClientID, cfg.CFAccessClientSecret = "cf-id", "cf-secret"
	}

	return newDevnetHandler(logrus.New(), cfg, upstream)
}

func serveDevnet(h http.Handler, method, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.Header.Set("Authorization", "Bearer caller-proxy-token")
	req.Header.Set("Cookie", "session=browser")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	return rec
}

func TestDevnetHandlerRejectsPaths(t *testing.T) {
	upstream := &devnetUpstream{}
	h := newTestDevnetHandler(DevnetConfig{Services: []string{"spamoor"}}, upstream)

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

	assert.Zero(t, upstream.mints, "rejected paths must not mint")
	assert.Empty(t, upstream.seen, "rejected paths must not reach a service")
}

func TestDevnetHandlerMintsCachesAndInjects(t *testing.T) {
	upstream := &devnetUpstream{}
	h := newTestDevnetHandler(DevnetConfig{}, upstream)

	for range 2 {
		rec := serveDevnet(h, http.MethodPost, "/devnet/spamoor/heze-devnet-2/api/spammer/7/pause?x=1")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}

	assert.Equal(t, 1, upstream.mints, "the token is cached per label")
	assert.Equal(t, "cf-id", upstream.mintHeader.Get("CF-Access-Client-Id"))
	assert.Equal(t, "cf-secret", upstream.mintHeader.Get("CF-Access-Client-Secret"))

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

	// A second devnet mints its own token.
	rec := serveDevnet(h, http.MethodGet, "/devnet/assertoor/glamsterdam-devnet-8/api/v1/tests")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 2, upstream.mints)
	assert.Equal(t, "assertoor.glamsterdam-devnet-8.ethpandaops.io", upstream.seen[2].Host)
}

func TestDevnetHandlerDomainSuffix(t *testing.T) {
	upstream := &devnetUpstream{}
	h := newTestDevnetHandler(DevnetConfig{DomainSuffix: "example.org"}, upstream)

	rec := serveDevnet(h, http.MethodGet, "/devnet/spamoor/dev-1/api/spammers")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "spamoor.dev-1.example.org", upstream.seen[0].Host)
}

func TestDevnetHandlerStaticTokenSkipsMinting(t *testing.T) {
	upstream := &devnetUpstream{}
	h := newTestDevnetHandler(DevnetConfig{StaticToken: "static"}, upstream)

	rec := serveDevnet(h, http.MethodDelete, "/devnet/spamoor/dev-1/api/spammer/3")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Zero(t, upstream.mints)
	assert.Equal(t, "Bearer static", upstream.seen[0].Header.Get("Authorization"))
}

func TestDevnetHandlerMintRedirectIsAnError(t *testing.T) {
	upstream := &devnetUpstream{mintStatus: http.StatusFound}
	h := newTestDevnetHandler(DevnetConfig{}, upstream)

	rec := serveDevnet(h, http.MethodGet, "/devnet/spamoor/dev-1/api/spammers")
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Contains(t, rec.Body.String(), "no service-token policy")
	assert.Empty(t, upstream.seen, "no token, no upstream call")
}

func TestDevnetHandlerUpstream401ForgetsToken(t *testing.T) {
	upstream := &devnetUpstream{upstream: http.StatusUnauthorized}
	h := newTestDevnetHandler(DevnetConfig{}, upstream)

	rec := serveDevnet(h, http.MethodPost, "/devnet/spamoor/dev-1/api/spammer/1/start")
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "upstream status passes through")

	upstream.upstream = 0
	rec = serveDevnet(h, http.MethodPost, "/devnet/spamoor/dev-1/api/spammer/1/start")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 2, upstream.mints, "a rejected token is re-minted")
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
