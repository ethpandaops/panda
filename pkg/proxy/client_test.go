package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/types"
)

// TestDiscoverFiresOnDiscoverHook verifies the OnDiscover callback fires after
// each successful Discover and observes the freshly committed datasources.
// This is the hook the app uses to propagate the proxy client's periodic
// refresh into module state without a server restart.
func TestDiscoverFiresOnDiscoverHook(t *testing.T) {
	t.Parallel()

	var version atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		current := version.Add(1)

		resp := datasourcesResponseWire{
			ClickHouse: []string{"a"},
		}
		if current >= 2 {
			resp.ClickHouse = []string{"a", "b"}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)

	var hookCalls atomic.Int32
	var lastSeen atomic.Int32

	log := logrus.New()
	log.SetOutput(io.Discard)

	client := NewClient(log, ClientConfig{
		URL: srv.URL,
		OnDiscover: func() {
			hookCalls.Add(1)
		},
	}).(*proxyClient)

	// We do not call Start() — the background goroutine isn't needed for this
	// test, and dodging it keeps the assertion deterministic.
	if err := client.Discover(context.Background()); err != nil {
		t.Fatalf("initial Discover error = %v", err)
	}

	lastSeen.Store(int32(len(client.ClickHouseDatasources())))
	if got := hookCalls.Load(); got != 1 {
		t.Fatalf("after initial Discover: hookCalls = %d, want 1", got)
	}

	if err := client.Discover(context.Background()); err != nil {
		t.Fatalf("second Discover error = %v", err)
	}

	if got := hookCalls.Load(); got != 2 {
		t.Fatalf("after second Discover: hookCalls = %d, want 2", got)
	}

	if got := client.ClickHouseDatasources(); len(got) != 2 || strings.Join(got, ",") != "a,b" {
		t.Fatalf("after second Discover: ClickHouseDatasources() = %v, want [a b]", got)
	}
}

// TestDiscoverNilOnDiscoverIsSafe verifies a nil OnDiscover hook does not
// panic the discovery goroutine.
func TestDiscoverNilOnDiscoverIsSafe(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(DatasourcesResponse{})
	}))
	t.Cleanup(srv.Close)

	log := logrus.New()
	log.SetOutput(io.Discard)

	client := NewClient(log, ClientConfig{URL: srv.URL}).(*proxyClient)

	if err := client.Discover(context.Background()); err != nil {
		t.Fatalf("Discover error = %v", err)
	}
}

// TestDiscoverRecoversFromOnDiscoverPanic verifies a panic inside the
// OnDiscover hook (e.g. a bug in module activation reacting to a newly
// discovered datasource) does not escape Discover and crash the process. The
// client must stay usable for subsequent calls afterward.
func TestDiscoverRecoversFromOnDiscoverPanic(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(DatasourcesResponse{})
	}))
	t.Cleanup(srv.Close)

	log := logrus.New()
	log.SetOutput(io.Discard)

	var hookCalls atomic.Int32

	client := NewClient(log, ClientConfig{
		URL: srv.URL,
		OnDiscover: func() {
			hookCalls.Add(1)
			panic("boom")
		},
	}).(*proxyClient)

	if err := client.Discover(context.Background()); err != nil {
		t.Fatalf("Discover error = %v", err)
	}

	if got := hookCalls.Load(); got != 1 {
		t.Fatalf("hookCalls after first Discover = %d, want 1", got)
	}

	// A second Discover proves the client is still usable after the panic.
	if err := client.Discover(context.Background()); err != nil {
		t.Fatalf("second Discover error = %v", err)
	}

	if got := hookCalls.Load(); got != 2 {
		t.Fatalf("hookCalls after second Discover = %d, want 2", got)
	}
}

func TestDatasourceInfoIncludesProxyName(t *testing.T) {
	t.Parallel()

	client := &proxyClient{
		cfg: ClientConfig{Name: "hosted"},
		datasources: &DatasourcesResponse{
			ClickHouseInfo: []types.DatasourceInfo{{Name: "xatu"}},
			PrometheusInfo: []types.DatasourceInfo{{Name: "metrics"}},
		},
	}

	clickhouse := client.ClickHouseDatasourceInfo()
	if len(clickhouse) != 1 {
		t.Fatalf("ClickHouseDatasourceInfo() length = %d, want 1", len(clickhouse))
	}
	if clickhouse[0].ProxyName != "hosted" {
		t.Fatalf("ClickHouseDatasourceInfo()[0].ProxyName = %q, want hosted", clickhouse[0].ProxyName)
	}

	prometheus := client.PrometheusDatasourceInfo()
	if len(prometheus) != 1 {
		t.Fatalf("PrometheusDatasourceInfo() length = %d, want 1", len(prometheus))
	}
	if prometheus[0].ProxyName != "hosted" {
		t.Fatalf("PrometheusDatasourceInfo()[0].ProxyName = %q, want hosted", prometheus[0].ProxyName)
	}
}

func TestClickHouseQueryUsesRequestContextDeadline(t *testing.T) {
	t.Parallel()

	const responseDelay = 75 * time.Millisecond

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(responseDelay)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)

	log := logrus.New()
	log.SetOutput(io.Discard)

	client := NewClient(log, ClientConfig{
		URL:         srv.URL,
		HTTPTimeout: 25 * time.Millisecond,
	}).(*proxyClient)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	start := time.Now()
	body, err := client.ClickHouseQuery(ctx, "xatu", "SELECT 1", nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("ClickHouseQuery error = %v", err)
	}
	if string(body) != "ok" {
		t.Fatalf("ClickHouseQuery body = %q, want ok", body)
	}
	if elapsed < responseDelay {
		t.Fatalf("ClickHouseQuery elapsed = %v, want at least %v", elapsed, responseDelay)
	}
}

func TestClickHouseQueryUsesHTTPTimeoutWithoutRequestDeadline(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)

	log := logrus.New()
	log.SetOutput(io.Discard)

	client := NewClient(log, ClientConfig{
		URL:         srv.URL,
		HTTPTimeout: 25 * time.Millisecond,
	}).(*proxyClient)

	start := time.Now()
	_, err := client.ClickHouseQuery(context.Background(), "xatu", "SELECT 1", nil)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("ClickHouseQuery error = nil, want timeout")
	}
	if elapsed >= time.Second {
		t.Fatalf("ClickHouseQuery elapsed = %v, want HTTPTimeout fallback to fire quickly", elapsed)
	}
}
