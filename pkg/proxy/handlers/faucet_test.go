package handlers

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFaucetUpstreamHost(t *testing.T) {
	assert.Equal(t,
		"faucet-agents.glamsterdam-devnet-6.ethpandaops.io",
		faucetUpstreamHost("glamsterdam-devnet-6"),
	)
}

// TestFaucetProxyRewrite verifies the Rewrite closure targets the credential-
// gated faucet host, strips the caller's bearer, and attaches basic auth.
func TestFaucetProxyRewrite(t *testing.T) {
	h := NewFaucetHandler(logrus.New(), FaucetConfig{Username: "u", Password: "p"})
	host := faucetUpstreamHost("net-1")

	rp := h.getOrCreateProxy(host)
	require.NotNil(t, rp.Rewrite, "handler must use Rewrite")

	in := httptest.NewRequest(http.MethodPost, "/api/startSession", nil)
	in.Header.Set("Authorization", "Bearer caller-token")
	pr := &httputil.ProxyRequest{In: in, Out: in.Clone(in.Context())}

	rp.Rewrite(pr)

	assert.Equal(t, "https", pr.Out.URL.Scheme)
	assert.Equal(t, host, pr.Out.URL.Host)
	assert.Equal(t, host, pr.Out.Host)

	// The caller's bearer is dropped and replaced with the faucet basic-auth.
	user, pass, ok := pr.Out.BasicAuth()
	assert.True(t, ok, "basic auth must be set")
	assert.Equal(t, "u", user)
	assert.Equal(t, "p", pass)
	assert.NotContains(t, pr.Out.Header.Get("Authorization"), "caller-token")
}

func TestFaucetServeHTTPValidation(t *testing.T) {
	h := NewFaucetHandler(logrus.New(), FaucetConfig{})

	for _, path := range []string{"/nope", "/faucet/", "/faucet/BAD_NET/api/x"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		assert.Equal(t, http.StatusBadRequest, rec.Code, "path %q should be rejected", path)
	}
}
