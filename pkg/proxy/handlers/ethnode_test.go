package handlers

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEthNodeProxyRewrite verifies the ethnode Rewrite closure (the part changed
// by the Director->Rewrite migration). The ethnode upstream is a real
// *.ethpandaops.io https host derived from the path, so rather than dialing it we
// exercise the Rewrite directly and assert it targets the right https host and
// rewrites auth + X-Forwarded. (Path parsing in ServeHTTP is unchanged.)
func TestEthNodeProxyRewrite(t *testing.T) {
	h := NewEthNodeHandler(logrus.New(), EthNodeConfig{Username: "user", Password: "pass"})

	const host = "bn-foo.srv.mainnet.ethpandaops.io"

	rp := h.getOrCreateProxy(host)
	require.NotNil(t, rp.Rewrite, "handler must use Rewrite (not the deprecated Director)")

	in := httptest.NewRequest(http.MethodGet, "/eth/v1/node/health", nil)
	in.Header.Set("Authorization", "Bearer sandbox-token")
	in.RemoteAddr = "203.0.113.11:4444"

	out := in.Clone(in.Context())
	out.RequestURI = ""

	pr := &httputil.ProxyRequest{In: in, Out: out}
	rp.Rewrite(pr)

	assert.Equal(t, "https", pr.Out.URL.Scheme) // beacon/exec nodes are https
	assert.Equal(t, host, pr.Out.URL.Host)      // target host from path
	assert.Equal(t, host, pr.Out.Host)          // Host header set to target
	assert.NotEqual(t, "Bearer sandbox-token", pr.Out.Header.Get("Authorization"))
	assert.True(t, strings.HasPrefix(pr.Out.Header.Get("Authorization"), "Basic "),
		"expected basic auth, got %q", pr.Out.Header.Get("Authorization"))
	assert.Equal(t, "203.0.113.11", pr.Out.Header.Get("X-Forwarded-For")) // SetXForwarded
}

func TestEthNodeUpstreamHost(t *testing.T) {
	tests := []struct {
		name     string
		mode     string
		network  string
		instance string
		want     string
	}{
		{
			name:     "beacon instance",
			mode:     "beacon",
			network:  "mainnet",
			instance: "lighthouse-1",
			want:     "bn-lighthouse-1.srv.mainnet.ethpandaops.io",
		},
		{
			name:     "execution instance",
			mode:     "execution",
			network:  "mainnet",
			instance: "geth-1",
			want:     "rpc-geth-1.srv.mainnet.ethpandaops.io",
		},
		{
			name:     "beacon load balanced",
			mode:     "beacon",
			network:  "mainnet",
			instance: "lb",
			want:     "bn.mainnet.ethpandaops.io",
		},
		{
			name:     "execution load balanced",
			mode:     "execution",
			network:  "mainnet",
			instance: "lb",
			want:     "rpc.mainnet.ethpandaops.io",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, ethNodeUpstreamHost(test.mode, test.network, test.instance))
		})
	}
}
