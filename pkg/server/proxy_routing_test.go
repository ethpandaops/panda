package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/attribution"
	"github.com/ethpandaops/panda/pkg/proxy"
	"github.com/ethpandaops/panda/pkg/proxy/handlers"
	"github.com/ethpandaops/panda/pkg/types"
)

func TestClickHouseQueryRoutesToDatasourceOwnerProxy(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{body: "ok\n", contentType: "text/plain"}
	svc := testRoutingService(t, transport, []proxy.ClientRoute{
		{
			Name: "hosted",
			Client: &routingProxyClient{
				url:        "https://hosted.proxy",
				token:      "hosted-token",
				clickhouse: []types.DatasourceInfo{{Name: "xatu"}},
			},
		},
		{
			Name:  "local",
			Local: true,
			Client: &routingProxyClient{
				url:        "https://local.proxy",
				token:      "local-token",
				clickhouse: []types.DatasourceInfo{{Name: "local-kurtosis"}},
			},
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/operations/clickhouse.query", strings.NewReader(`{
		"args": {
			"datasource": "local-kurtosis",
			"sql": "SELECT 1"
		}
	}`))
	rec := httptest.NewRecorder()

	svc.handleClickHouseQuery(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q, want 200", rec.Code, rec.Body.String())
	}
	if got := transport.last.URL.Host; got != "local.proxy" {
		t.Fatalf("proxied host = %q, want local.proxy", got)
	}
	if got := transport.last.Header.Get("Authorization"); got != "Bearer local-token" {
		t.Fatalf("Authorization = %q, want local token", got)
	}
	if got := transport.last.Header.Get(handlers.DatasourceHeader); got != "local-kurtosis" {
		t.Fatalf("%s = %q, want local-kurtosis", handlers.DatasourceHeader, got)
	}
	if got := strings.TrimSpace(string(transport.lastBody)); got != "SELECT 1" {
		t.Fatalf("proxied body = %q, want SELECT 1", got)
	}
}

func TestDatasourceProxyRequestRoutesByTypeAndName(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{body: `{"status":"success"}`, contentType: "application/json"}
	svc := testRoutingService(t, transport, []proxy.ClientRoute{
		{
			Name: "hosted",
			Client: &routingProxyClient{
				url:        "https://hosted.proxy",
				token:      "hosted-token",
				prometheus: []types.DatasourceInfo{{Name: "metrics"}},
			},
		},
		{
			Name:  "local",
			Local: true,
			Client: &routingProxyClient{
				url:   "https://local.proxy",
				token: "local-token",
				loki:  []types.DatasourceInfo{{Name: "logs"}},
			},
		},
	})

	_, status, _, err := svc.proxyDatasourceRequest(
		context.Background(),
		"loki",
		"logs",
		http.MethodGet,
		"/loki/loki/api/v1/query?query=up",
		nil,
		http.Header{handlers.DatasourceHeader: []string{"logs"}},
	)
	if err != nil {
		t.Fatalf("proxyDatasourceRequest error = %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if got := transport.last.URL.Host; got != "local.proxy" {
		t.Fatalf("proxied host = %q, want local.proxy", got)
	}
	if got := transport.last.Header.Get("Authorization"); got != "Bearer local-token" {
		t.Fatalf("Authorization = %q, want local token", got)
	}
}

func TestPrimaryProxyRequestUsesFirstExternalProxy(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{body: `{}`, contentType: "application/json"}
	svc := testRoutingService(t, transport, []proxy.ClientRoute{
		{
			Name:  "local",
			Local: true,
			Client: &routingProxyClient{
				url:   "https://local.proxy",
				token: "local-token",
			},
		},
		{
			Name: "hosted",
			Client: &routingProxyClient{
				url:   "https://hosted.proxy",
				token: "hosted-token",
			},
		},
	})

	_, status, _, err := svc.proxyRequest(
		context.Background(),
		http.MethodPost,
		"/github/actions/trigger",
		strings.NewReader(`{}`),
		http.Header{"Content-Type": []string{"application/json"}},
	)
	if err != nil {
		t.Fatalf("proxyRequest error = %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if got := transport.last.URL.Host; got != "hosted.proxy" {
		t.Fatalf("proxied host = %q, want hosted.proxy", got)
	}
	if got := transport.last.Header.Get("Authorization"); got != "Bearer hosted-token" {
		t.Fatalf("Authorization = %q, want hosted token", got)
	}
}

func TestPrimaryProxyRequestWithoutExternalProxyIsUnavailable(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{body: `{}`, contentType: "application/json"}
	svc := testRoutingService(t, transport, []proxy.ClientRoute{
		{
			Name:  "local",
			Local: true,
			Client: &routingProxyClient{
				url:   "https://local.proxy",
				token: "local-token",
			},
		},
	})

	_, status, _, err := svc.proxyRequest(context.Background(), http.MethodGet, "/beacon/mainnet/node/eth/v1/node/version", nil, nil)
	if err == nil {
		t.Fatalf("proxyRequest error = nil, want unavailable error")
	}
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", status)
	}
	if transport.last != nil {
		t.Fatalf("transport was called for unavailable primary")
	}
}

func testRoutingService(t *testing.T, transport http.RoundTripper, routes []proxy.ClientRoute) *service {
	t.Helper()

	log := logrus.New()
	log.SetOutput(io.Discard)

	return &service{
		log:          log,
		proxyService: proxy.NewRouter(log, routes),
		httpClient:   &http.Client{Transport: transport},
	}
}

type recordingTransport struct {
	status      int
	body        string
	contentType string

	last     *http.Request
	lastBody []byte
}

func (t *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.last = req.Clone(req.Context())

	if req.Body != nil {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		t.lastBody = body
	}

	status := t.status
	if status == 0 {
		status = http.StatusOK
	}

	resp := &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewBufferString(t.body)),
	}
	if t.contentType != "" {
		resp.Header.Set("Content-Type", t.contentType)
	}

	return resp, nil
}

type routingProxyClient struct {
	url   string
	token string

	clickhouse   []types.DatasourceInfo
	prometheus   []types.DatasourceInfo
	loki         []types.DatasourceInfo
	benchmarkoor []types.DatasourceInfo
}

func (c *routingProxyClient) Start(_ context.Context) error { return nil }
func (c *routingProxyClient) Stop(_ context.Context) error  { return nil }
func (c *routingProxyClient) URL() string                   { return c.url }
func (c *routingProxyClient) RegisterToken() string         { return c.token }
func (c *routingProxyClient) RevokeToken()                  {}
func (c *routingProxyClient) ClickHouseDatasources() []string {
	return datasourceNames(c.ClickHouseDatasourceInfo())
}
func (c *routingProxyClient) ClickHouseDatasourceInfo() []types.DatasourceInfo {
	return append([]types.DatasourceInfo(nil), c.clickhouse...)
}
func (c *routingProxyClient) PrometheusDatasources() []string {
	return datasourceNames(c.PrometheusDatasourceInfo())
}
func (c *routingProxyClient) PrometheusDatasourceInfo() []types.DatasourceInfo {
	return append([]types.DatasourceInfo(nil), c.prometheus...)
}
func (c *routingProxyClient) LokiDatasources() []string {
	return datasourceNames(c.LokiDatasourceInfo())
}
func (c *routingProxyClient) LokiDatasourceInfo() []types.DatasourceInfo {
	return append([]types.DatasourceInfo(nil), c.loki...)
}
func (c *routingProxyClient) BenchmarkoorDatasourceInfo() []types.DatasourceInfo {
	return append([]types.DatasourceInfo(nil), c.benchmarkoor...)
}
func (c *routingProxyClient) EthNodeAvailable() bool { return false }
func (c *routingProxyClient) EthNodeDatasourceInfo() []types.DatasourceInfo {
	return nil
}
func (c *routingProxyClient) EmbeddingAvailable() bool         { return false }
func (c *routingProxyClient) EmbeddingModel() string           { return "" }
func (c *routingProxyClient) Discover(_ context.Context) error { return nil }
func (c *routingProxyClient) EnsureAuthenticated(_ context.Context) error {
	return nil
}

func (c *routingProxyClient) ClickHouseQuery(_ context.Context, _, _ string, _ url.Values) ([]byte, error) {
	return nil, nil
}

func datasourceNames(infos []types.DatasourceInfo) []string {
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		names = append(names, info.Name)
	}

	return names
}

var _ proxy.Client = (*routingProxyClient)(nil)

func TestProxyRequestForwardsAttribution(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{body: "ok\n", contentType: "text/plain"}
	svc := testRoutingService(t, transport, []proxy.ClientRoute{
		{
			Name: "hosted",
			Client: &routingProxyClient{
				url:        "https://hosted.proxy",
				token:      "hosted-token",
				clickhouse: []types.DatasourceInfo{{Name: "xatu"}},
			},
		},
	})

	ctx := attribution.WithValue(context.Background(), "discord:sam")

	_, status, _, err := svc.proxyDatasourceRequest(
		ctx,
		"clickhouse",
		"xatu",
		http.MethodPost,
		"/clickhouse/",
		strings.NewReader("SELECT 1"),
		http.Header{handlers.DatasourceHeader: []string{"xatu"}},
	)
	if err != nil {
		t.Fatalf("proxyDatasourceRequest error = %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if got := transport.last.Header.Get(attribution.Header); got != "discord:sam" {
		t.Fatalf("%s = %q, want discord:sam", attribution.Header, got)
	}
}

func TestAttributionMiddlewareLiftsHeader(t *testing.T) {
	t.Parallel()

	var seen string

	handler := attributionMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = attribution.FromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/datasources", nil)
	req.Header.Set(attribution.Header, "discord:sam")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if seen != "discord:sam" {
		t.Fatalf("attribution in context = %q, want discord:sam", seen)
	}
}
