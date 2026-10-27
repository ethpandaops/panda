package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethpandaops/cartographoor/pkg/discovery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/proxy"
	"github.com/ethpandaops/panda/pkg/proxy/handlers"
	"github.com/ethpandaops/panda/pkg/types"
)

var devnetTestSpamoor = types.DatasourceInfo{
	Type: "devnet", Name: "spamoor", Metadata: map[string]string{"domain_suffix": "ethpandaops.io"},
}

func devnetTestService(t *testing.T, transport http.RoundTripper, relayed ...types.DatasourceInfo) *service {
	t.Helper()

	svc := testRoutingService(t, transport, []proxy.ClientRoute{{
		Name:   "hosted",
		Client: &routingProxyClient{url: "https://hosted.proxy", token: "hosted-token", devnet: relayed},
	}})
	svc.cartographoorClient = tracoorOperationCartographoor{networks: map[string]discovery.Network{
		// Cartographoor names need not match the DNS label.
		"focil-devnet-2": {ServiceURLs: &discovery.ServiceURLs{
			Spamoor:   "https://spamoor.heze-devnet-2.ethpandaops.io",
			Assertoor: "https://assertoor.heze-devnet-2.ethpandaops.io",
		}},
		"odd-devnet": {ServiceURLs: &discovery.ServiceURLs{Spamoor: "https://spammer.odd.example.com"}},
		"mainnet":    {ServiceURLs: &discovery.ServiceURLs{Dora: "https://dora.ethpandaops.io"}},
	}}

	return svc
}

func postDevnetOperation(svc *service, operation, args string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/operations/"+operation, strings.NewReader(`{"args":`+args+`}`))
	rec := httptest.NewRecorder()
	svc.handleDevnetOperation(operation, rec, req)

	return rec
}

func TestDevnetLabel(t *testing.T) {
	tests := []struct {
		url, service, want string
		wantErr            bool
	}{
		{"https://spamoor.heze-devnet-2.ethpandaops.io", "spamoor", "heze-devnet-2", false},
		{"https://spamoor.heze-devnet-2.ethpandaops.io/", "spamoor", "heze-devnet-2", false},
		{"https://assertoor.glamsterdam-devnet-8.ethpandaops.io", "spamoor", "", true},
		{"https://spamoor.a.b.ethpandaops.io", "spamoor", "", true},
		{"https://spamoor.devnet.example.com", "spamoor", "", true},
		{"http://spamoor.devnet.ethpandaops.io", "spamoor", "", true},
		{"https://spamoor.devnet.ethpandaops.io/ui", "spamoor", "", true},
		{"https://spamoor.devnet.ethpandaops.io:8443", "spamoor", "", true},
		{"https://spamoor.ethpandaops.io", "spamoor", "", true},
	}

	for _, tt := range tests {
		got, err := devnetLabel(tt.url, tt.service, "ethpandaops.io")
		if tt.wantErr {
			assert.Error(t, err, tt.url)

			continue
		}

		require.NoError(t, err, tt.url)
		assert.Equal(t, tt.want, got, tt.url)
	}
}

func TestDevnetAPIPath(t *testing.T) {
	for _, ok := range []string{"/api/spammers", "/api/v1/test_run/4/status", "/api/spammer/1/"} {
		_, err := devnetAPIPath(ok)
		assert.NoError(t, err, ok)
	}

	for _, bad := range []string{
		"/metrics", "api/spammers", "/api/../metrics", "/api//x", "/api/spammers?x=1",
		"/api/spammers/events", "/api/spammer/1/logs/stream", "/api/v1/events/stream", "/api/v1/test_run/1/events",
	} {
		_, err := devnetAPIPath(bad)
		assert.Error(t, err, bad)
	}
}

func TestDevnetRequestRelaysThroughOwningProxy(t *testing.T) {
	transport := &recordingTransport{body: `{"ok":true}`, contentType: "application/json"}
	svc := devnetTestService(t, transport, devnetTestSpamoor)

	rec := postDevnetOperation(svc, "devnet.request", `{
		"network": "focil-devnet-2", "service": "spamoor", "method": "post",
		"path": "/api/spammer/12/start", "query": {"force": true}, "body": {"name": "x"}
	}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"ok":true}`, rec.Body.String())
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	out := transport.last
	assert.Equal(t, http.MethodPost, out.Method)
	assert.Equal(t, "hosted.proxy", out.URL.Host)
	assert.Equal(t, "/devnet/spamoor/heze-devnet-2/api/spammer/12/start", out.URL.Path)
	assert.Equal(t, "force=true", out.URL.RawQuery)
	assert.Equal(t, "spamoor", out.Header.Get(handlers.DatasourceHeader))
	assert.Equal(t, "application/json", out.Header.Get("Content-Type"))
	assert.Equal(t, "Bearer hosted-token", out.Header.Get("Authorization"))
	assert.JSONEq(t, `{"name":"x"}`, string(transport.lastBody))
}

func TestDevnetRequestFailsFast(t *testing.T) {
	tests := []struct {
		name    string
		args    string
		relayed []types.DatasourceInfo
		status  int
		message string
	}{
		{"service not relayed", `{"network":"focil-devnet-2","service":"assertoor","path":"/api/v1/tests"}`,
			[]types.DatasourceInfo{devnetTestSpamoor}, http.StatusForbidden, "allowed_orgs"},
		{"unknown network", `{"network":"nope","service":"spamoor","path":"/api/spammers"}`,
			[]types.DatasourceInfo{devnetTestSpamoor}, http.StatusNotFound, "unknown network"},
		{"network without service", `{"network":"mainnet","service":"spamoor","path":"/api/spammers"}`,
			[]types.DatasourceInfo{devnetTestSpamoor}, http.StatusNotFound, "has no spamoor service"},
		{"unexpected host", `{"network":"odd-devnet","service":"spamoor","path":"/api/spammers"}`,
			[]types.DatasourceInfo{devnetTestSpamoor}, http.StatusBadGateway, "is not of the form"},
		{"bad method", `{"network":"focil-devnet-2","service":"spamoor","method":"TRACE","path":"/api/spammers"}`,
			[]types.DatasourceInfo{devnetTestSpamoor}, http.StatusBadRequest, "unsupported method"},
		{"streaming path", `{"network":"focil-devnet-2","service":"spamoor","path":"/api/spammers/events"}`,
			[]types.DatasourceInfo{devnetTestSpamoor}, http.StatusBadRequest, "not supported through the relay"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &recordingTransport{body: `{}`}
			rec := postDevnetOperation(devnetTestService(t, transport, tt.relayed...), "devnet.request", tt.args)

			assert.Equal(t, tt.status, rec.Code)
			assert.Contains(t, rec.Body.String(), tt.message)
			assert.Nil(t, transport.last, "nothing is proxied")
		})
	}
}

func TestDevnetRequestErrorRemedies(t *testing.T) {
	transport := &recordingTransport{status: http.StatusUnauthorized, body: "unauthorized"}
	rec := postDevnetOperation(devnetTestService(t, transport, devnetTestSpamoor), "devnet.request",
		`{"network":"focil-devnet-2","service":"spamoor","method":"POST","path":"/api/spammer/1/pause"}`)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), "devnet_services credential")

	transport = &recordingTransport{status: http.StatusForbidden, body: "forbidden: insufficient org membership for this datasource\n"}
	rec = postDevnetOperation(devnetTestService(t, transport, devnetTestSpamoor), "devnet.request",
		`{"network":"focil-devnet-2","service":"spamoor","path":"/api/spammers"}`)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "not in the allowed_orgs for the spamoor relay")
}

func TestDevnetListNetworksMarksRelayedServices(t *testing.T) {
	rec := postDevnetOperation(devnetTestService(t, &recordingTransport{}, devnetTestSpamoor), "devnet.list_networks", `{}`)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Data struct {
			Networks []listItem `json:"networks"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	names := make([]string, 0, len(resp.Data.Networks))
	for _, n := range resp.Data.Networks {
		names = append(names, n.Name)
	}

	assert.Equal(t, []string{"focil-devnet-2", "odd-devnet"}, names, "networks without spamoor/assertoor are omitted")

	services, _ := resp.Data.Networks[0].Extra["services"].(map[string]any)
	spamoor, _ := services["spamoor"].(map[string]any)
	assertoor, _ := services["assertoor"].(map[string]any)
	assert.Equal(t, true, spamoor["relayed"])
	assert.Equal(t, false, assertoor["relayed"])
	assert.Equal(t, "https://assertoor.heze-devnet-2.ethpandaops.io", assertoor["url"])
}
