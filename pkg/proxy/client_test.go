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

	"github.com/sirupsen/logrus"
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

		resp := DatasourcesResponse{
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
