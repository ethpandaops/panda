package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newBuildoorTestLogger() logrus.FieldLogger {
	log := logrus.New()
	log.SetOutput(io.Discard)

	return log
}

// stubBuildoorUpstream repoints the handler's cached reverse proxy for host at
// the given test server, keeping the handler's own Rewrite (auth injection).
func stubBuildoorUpstream(h *BuildoorHandler, host string, upstream *httptest.Server) {
	target, _ := url.Parse(upstream.URL)
	rp := h.getOrCreateProxy(host)

	original := rp.Rewrite
	h.proxies[host] = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			original(pr)
			pr.Out.URL.Scheme = target.Scheme
			pr.Out.URL.Host = target.Host
		},
	}
}

func TestBuildoorHandlerStaticTokenInjection(t *testing.T) {
	t.Parallel()

	var gotAuth string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")

		assert.Equal(t, "/api/buildoor/action-plan", r.URL.Path)
		_, _ = w.Write([]byte(`{"status":"updated"}`))
	}))
	t.Cleanup(upstream.Close)

	h := NewBuildoorHandler(newBuildoorTestLogger(), BuildoorConfig{StaticToken: "static-secret"})
	stubBuildoorUpstream(h, "api-buildoor-prysm-ethrex-1.srv.testnet.ethpandaops.io", upstream)

	req := httptest.NewRequest(http.MethodPost, "/buildoor/testnet/prysm-ethrex-1/api/buildoor/action-plan", nil)
	req.Header.Set("Authorization", "Bearer caller-proxy-token")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	// The caller's proxy bearer must never reach the upstream.
	assert.Equal(t, "Bearer static-secret", gotAuth)
}

func TestBuildoorHandlerMintsAndCachesToken(t *testing.T) {
	t.Parallel()

	var mints atomic.Int32

	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/auth/token", r.URL.Path)
		assert.Equal(t, "svc-id", r.Header.Get("CF-Access-Client-Id"))
		assert.Equal(t, "svc-secret", r.Header.Get("CF-Access-Client-Secret"))

		mints.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token": "minted-jwt",
			"user":  "panda-proxy-svc",
			"expr":  time.Now().Add(30 * time.Minute).Unix(),
		})
	}))
	t.Cleanup(authSrv.Close)

	var gotAuth string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(upstream.Close)

	h := NewBuildoorHandler(newBuildoorTestLogger(), BuildoorConfig{
		CFAccessClientID:     "svc-id",
		CFAccessClientSecret: "svc-secret",
	})

	// Point the authenticatoor call at the test server.
	h.httpClient = &http.Client{Transport: rewriteHostTransport{target: authSrv.URL}}
	stubBuildoorUpstream(h, "api-buildoor-prysm-ethrex-1.srv.testnet.ethpandaops.io", upstream)

	for range 3 {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/buildoor/testnet/prysm-ethrex-1/api/buildoor/overview", nil))
		require.Equal(t, http.StatusOK, rec.Code)
	}

	assert.Equal(t, "Bearer minted-jwt", gotAuth)
	// The JWT is cached until near expiry — one mint serves all three requests.
	assert.Equal(t, int32(1), mints.Load())
}

func TestBuildoorHandlerRejectsBadPaths(t *testing.T) {
	t.Parallel()

	h := NewBuildoorHandler(newBuildoorTestLogger(), BuildoorConfig{StaticToken: "x"})

	for name, path := range map[string]string{
		"missing instance":  "/buildoor/testnet",
		"missing rest":      "/buildoor/testnet/prysm-ethrex-1",
		"non-api path":      "/buildoor/testnet/prysm-ethrex-1/metrics",
		"invalid network":   "/buildoor/Bad_Network/prysm-ethrex-1/api/buildoor/overview",
		"invalid instance":  "/buildoor/testnet/UPPER/api/buildoor/overview",
		"traversal segment": "/buildoor/testnet/../api/buildoor/overview",
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			assert.Equal(t, http.StatusBadRequest, rec.Code, "path %q", path)
		})
	}
}

func TestBuildoorHandlerNoCredentialConfigured(t *testing.T) {
	t.Parallel()

	h := NewBuildoorHandler(newBuildoorTestLogger(), BuildoorConfig{})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/buildoor/testnet/prysm-ethrex-1/api/buildoor/overview", nil))

	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Contains(t, rec.Body.String(), "no buildoor credential configured")
}

// rewriteHostTransport sends every request to the fixed target, preserving the
// request path — it stands in for DNS in tests.
type rewriteHostTransport struct {
	target string
}

func (t rewriteHostTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	target, err := url.Parse(t.target)
	if err != nil {
		return nil, err
	}

	r.URL.Scheme = target.Scheme
	r.URL.Host = target.Host

	return http.DefaultTransport.RoundTrip(r)
}
