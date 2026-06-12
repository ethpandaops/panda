package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/types"
)

func TestRouterMergesDatasourcesFirstWinsAndWarnsOnCollision(t *testing.T) {
	t.Parallel()

	var logBuf bytes.Buffer
	log := logrus.New()
	log.SetOutput(&logBuf)
	log.SetLevel(logrus.WarnLevel)
	log.SetFormatter(&logrus.TextFormatter{DisableTimestamp: true})

	hosted := &fakeRouterClient{
		url:        "https://hosted.example",
		token:      "hosted-token",
		clickhouse: []types.DatasourceInfo{{Name: "xatu"}},
		prometheus: []types.DatasourceInfo{{Name: "shared"}},
	}
	local := &fakeRouterClient{
		url:        "http://local.example",
		token:      "local-token",
		clickhouse: []types.DatasourceInfo{{Name: "xatu"}, {Name: "local-kurtosis"}},
		loki:       []types.DatasourceInfo{{Name: "shared"}},
	}

	router := NewRouter(log, []ClientRoute{
		{Name: "hosted", Client: hosted},
		{Name: "local", Client: local, Local: true},
	})

	clickhouse := router.ClickHouseDatasourceInfo()
	if got, want := namesFromInfo(clickhouse), []string{"xatu", "local-kurtosis"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("ClickHouseDatasourceInfo names = %v, want %v", got, want)
	}

	if clickhouse[0].ProxyName != "hosted" {
		t.Fatalf("xatu ProxyName = %q, want hosted", clickhouse[0].ProxyName)
	}
	if clickhouse[1].ProxyName != "local" {
		t.Fatalf("local-kurtosis ProxyName = %q, want local", clickhouse[1].ProxyName)
	}

	prometheus := router.PrometheusDatasourceInfo()
	if len(prometheus) != 1 || prometheus[0].Name != "shared" || prometheus[0].ProxyName != "hosted" {
		t.Fatalf("PrometheusDatasourceInfo() = %#v, want hosted shared datasource", prometheus)
	}

	loki := router.LokiDatasourceInfo()
	if len(loki) != 1 || loki[0].Name != "shared" || loki[0].ProxyName != "local" {
		t.Fatalf("LokiDatasourceInfo() = %#v, want local shared datasource", loki)
	}

	owner, ok := router.OwnerForDatasource("clickhouse", "xatu")
	if !ok {
		t.Fatalf("OwnerForDatasource(clickhouse, xatu) not found")
	}
	if owner.ProxyName != "hosted" || owner.URL != "https://hosted.example" {
		t.Fatalf("OwnerForDatasource(clickhouse, xatu) = %#v, want hosted", owner)
	}

	client, ok := router.ClientForDatasource("clickhouse", "local-kurtosis")
	if !ok {
		t.Fatalf("ClientForDatasource(clickhouse, local-kurtosis) not found")
	}
	if got := client.RegisterToken(); got != "local-token" {
		t.Fatalf("local-kurtosis owner token = %q, want local-token", got)
	}

	if _, err := router.ClickHouseQuery(context.Background(), "local-kurtosis", "SELECT 1", nil); err != nil {
		t.Fatalf("ClickHouseQuery(local-kurtosis) error = %v", err)
	}
	if hosted.queries != 0 || local.queries != 1 {
		t.Fatalf("query counts = hosted:%d local:%d, want hosted:0 local:1", hosted.queries, local.queries)
	}

	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "Datasource collision across proxies; keeping first") {
		t.Fatalf("collision warning missing from log output: %q", logOutput)
	}
	if !strings.Contains(logOutput, "winner_proxy=hosted") || !strings.Contains(logOutput, "ignored_proxy=local") {
		t.Fatalf("collision warning fields missing from log output: %q", logOutput)
	}
}

func TestRouterPrimaryIsFirstExternalProxy(t *testing.T) {
	t.Parallel()

	log := logrus.New()
	log.SetOutput(io.Discard)

	local := &fakeRouterClient{
		url:       "http://local.example",
		token:     "local-token",
		embedding: true,
		model:     "local-model",
		ethnode:   true,
	}
	hosted := &fakeRouterClient{
		url:       "https://hosted.example",
		token:     "hosted-token",
		embedding: true,
		model:     "hosted-model",
		ethnode:   true,
	}

	router := NewRouter(log, []ClientRoute{
		{Name: "local", Client: local, Local: true},
		{Name: "hosted", Client: hosted},
	})

	if router.Primary() != hosted {
		t.Fatalf("Primary() = %#v, want hosted client", router.Primary())
	}
	if got := router.URL(); got != "https://hosted.example" {
		t.Fatalf("URL() = %q, want hosted URL", got)
	}
	if got := router.RegisterToken(); got != "hosted-token" {
		t.Fatalf("RegisterToken() = %q, want hosted token", got)
	}
	if !router.EmbeddingAvailable() {
		t.Fatalf("EmbeddingAvailable() = false, want true from hosted primary")
	}
	if got := router.EmbeddingModel(); got != "hosted-model" {
		t.Fatalf("EmbeddingModel() = %q, want hosted-model", got)
	}
	if !router.EthNodeAvailable() {
		t.Fatalf("EthNodeAvailable() = false, want true from hosted primary")
	}
}

func TestRouterWithOnlyLocalProxyHasNoPrimary(t *testing.T) {
	t.Parallel()

	log := logrus.New()
	log.SetOutput(io.Discard)

	local := &fakeRouterClient{
		url:       "http://local.example",
		token:     "local-token",
		embedding: true,
		model:     "local-model",
		ethnode:   true,
	}

	router := NewRouter(log, []ClientRoute{
		{Name: "local", Client: local, Local: true},
	})

	if router.Primary() != nil {
		t.Fatalf("Primary() = %#v, want nil", router.Primary())
	}
	if got := router.URL(); got != "" {
		t.Fatalf("URL() = %q, want empty", got)
	}
	if got := router.RegisterToken(); got != "" {
		t.Fatalf("RegisterToken() = %q, want empty", got)
	}
	if router.EmbeddingAvailable() {
		t.Fatalf("EmbeddingAvailable() = true, want false without external primary")
	}
	if got := router.EmbeddingModel(); got != "" {
		t.Fatalf("EmbeddingModel() = %q, want empty", got)
	}
	if router.EthNodeAvailable() {
		t.Fatalf("EthNodeAvailable() = true, want false without external primary")
	}
	if err := router.EnsureAuthenticated(context.Background()); err != nil {
		t.Fatalf("EnsureAuthenticated() error = %v, want nil without external primary", err)
	}
}

func TestRouterStartsStopsAndDiscoversAllClients(t *testing.T) {
	t.Parallel()

	log := logrus.New()
	log.SetOutput(io.Discard)

	first := &fakeRouterClient{}
	second := &fakeRouterClient{}

	router := NewRouter(log, []ClientRoute{
		{Name: "first", Client: first},
		{Name: "second", Client: second},
	})

	if err := router.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := router.Discover(context.Background()); err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if err := router.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	if first.starts != 1 || second.starts != 1 {
		t.Fatalf("starts = (%d, %d), want (1, 1)", first.starts, second.starts)
	}
	if first.discovers != 1 || second.discovers != 1 {
		t.Fatalf("discovers = (%d, %d), want (1, 1)", first.discovers, second.discovers)
	}
	if first.stops != 1 || second.stops != 1 {
		t.Fatalf("stops = (%d, %d), want (1, 1)", first.stops, second.stops)
	}
}

func TestRouterStartContinuesAfterInitialDiscoveryFailure(t *testing.T) {
	t.Parallel()

	log := logrus.New()
	log.SetOutput(io.Discard)

	unreachable := NewClient(log, ClientConfig{
		Name: "hosted",
		URL:  "http://unreachable.proxy",
	}).(*proxyClient)
	unreachable.cfg.DiscoveryInterval = 0
	unreachable.httpClient = &http.Client{Transport: failingRoundTripper{}}

	reachable := &fakeRouterClient{
		clickhouse: []types.DatasourceInfo{{
			Name:        "local-kurtosis",
			Description: "Local datasource",
		}},
	}

	router := NewRouter(log, []ClientRoute{
		{Name: "hosted", Client: unreachable},
		{Name: "local", Client: reachable, Local: true},
	})

	if err := router.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v, want nil when initial discovery fails", err)
	}
	defer func() { _ = router.Stop(context.Background()) }()

	clickhouse := router.ClickHouseDatasourceInfo()
	if len(clickhouse) != 1 {
		t.Fatalf("ClickHouseDatasourceInfo() length = %d, want 1", len(clickhouse))
	}
	if clickhouse[0].Name != "local-kurtosis" || clickhouse[0].ProxyName != "local" {
		t.Fatalf("ClickHouseDatasourceInfo()[0] = %#v, want local local-kurtosis", clickhouse[0])
	}
	if clickhouse[0].Description != "Local datasource" {
		t.Fatalf("ClickHouseDatasourceInfo()[0].Description = %q, want Local datasource", clickhouse[0].Description)
	}
}

type fakeRouterClient struct {
	url   string
	token string

	clickhouse []types.DatasourceInfo
	prometheus []types.DatasourceInfo
	loki       []types.DatasourceInfo

	benchmarkoor []types.DatasourceInfo

	ethnode   bool
	embedding bool
	model     string

	starts    int
	stops     int
	discovers int
	queries   int
}

func (f *fakeRouterClient) Start(_ context.Context) error {
	f.starts++

	return nil
}

func (f *fakeRouterClient) Stop(_ context.Context) error {
	f.stops++

	return nil
}

func (f *fakeRouterClient) URL() string { return f.url }

func (f *fakeRouterClient) RegisterToken() string { return f.token }

func (f *fakeRouterClient) RevokeToken() {}

func (f *fakeRouterClient) ClickHouseDatasources() []string {
	return namesFromInfo(f.ClickHouseDatasourceInfo())
}

func (f *fakeRouterClient) ClickHouseDatasourceInfo() []types.DatasourceInfo {
	return append([]types.DatasourceInfo(nil), f.clickhouse...)
}

func (f *fakeRouterClient) ClickHouseQuery(_ context.Context, _, _ string, _ url.Values) ([]byte, error) {
	f.queries++

	return nil, nil
}

func (f *fakeRouterClient) PrometheusDatasources() []string {
	return namesFromInfo(f.PrometheusDatasourceInfo())
}

func (f *fakeRouterClient) PrometheusDatasourceInfo() []types.DatasourceInfo {
	return append([]types.DatasourceInfo(nil), f.prometheus...)
}

func (f *fakeRouterClient) LokiDatasources() []string {
	return namesFromInfo(f.LokiDatasourceInfo())
}

func (f *fakeRouterClient) LokiDatasourceInfo() []types.DatasourceInfo {
	return append([]types.DatasourceInfo(nil), f.loki...)
}

func (f *fakeRouterClient) BenchmarkoorDatasourceInfo() []types.DatasourceInfo {
	return append([]types.DatasourceInfo(nil), f.benchmarkoor...)
}

func (f *fakeRouterClient) EthNodeAvailable() bool { return f.ethnode }

func (f *fakeRouterClient) EthNodeDatasourceInfo() []types.DatasourceInfo {
	return ethNodeDatasourceInfo(f.ethnode)
}

func (f *fakeRouterClient) EmbeddingAvailable() bool { return f.embedding }

func (f *fakeRouterClient) EmbeddingModel() string { return f.model }

func (f *fakeRouterClient) Discover(_ context.Context) error {
	f.discovers++

	return nil
}

func (f *fakeRouterClient) EnsureAuthenticated(_ context.Context) error { return nil }

var _ Client = (*fakeRouterClient)(nil)

type failingRoundTripper struct{}

func (failingRoundTripper) RoundTrip(_ *http.Request) (*http.Response, error) {
	return nil, errors.New("dial failed")
}
