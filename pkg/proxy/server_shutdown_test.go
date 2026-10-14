package proxy

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

func quietProxyTestLogger() *logrus.Logger {
	l := logrus.New()
	l.SetLevel(logrus.PanicLevel)

	return l
}

func newRunningTestServer(t *testing.T) *server {
	t.Helper()

	cfg := ServerConfig{
		Server: HTTPServerConfig{ListenAddr: "127.0.0.1:0"},
		Auth:   AuthConfig{Mode: AuthModeNone},
		ClickHouse: []ClickHouseClusterConfig{
			{
				BaseDatasourceConfig: BaseDatasourceConfig{Name: "xatu"},
				Host:                 "example.com",
				Port:                 8123,
				Database:             "default",
			},
		},
	}
	cfg.ApplyDefaults()

	srv, err := newServer(quietProxyTestLogger(), cfg, "http://127.0.0.1", "0")
	if err != nil {
		t.Fatalf("newServer failed: %v", err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	return srv
}

// TestStopReturnsPromptlyUnderConcurrentReadyTraffic drives a steady stream of
// /ready requests against a running server and confirms Stop still returns
// well within its budget. /ready reads readiness off an atomic flag rather
// than the same lock Stop holds while tearing the server down, so a request
// in flight during shutdown can never keep that lock held open.
func TestStopReturnsPromptlyUnderConcurrentReadyTraffic(t *testing.T) {
	t.Parallel()

	srv := newRunningTestServer(t)
	base := srv.URL()
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 64}}

	stop := make(chan struct{})
	var traffic sync.WaitGroup

	for i := 0; i < 32; i++ {
		traffic.Add(1)

		go func() {
			defer traffic.Done()

			for {
				select {
				case <-stop:
					return
				default:
				}

				resp, err := client.Get(base + "/ready")
				if err == nil {
					_ = resp.Body.Close()
				}
			}
		}()
	}

	// Let traffic ramp up before shutting down.
	time.Sleep(100 * time.Millisecond)

	const shutdownBudget = 3 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), shutdownBudget)
	defer cancel()

	start := time.Now()
	err := srv.Stop(ctx)
	elapsed := time.Since(start)

	close(stop)
	traffic.Wait()

	if err != nil {
		t.Fatalf("Stop returned an error: %v", err)
	}
	if elapsed >= shutdownBudget/2 {
		t.Fatalf("Stop took %s against a %s budget while /ready traffic was in flight; want well under half", elapsed, shutdownBudget)
	}
}

// TestStopIsIdempotentUnderConcurrentCallers confirms two concurrent Stop
// calls on the same server are safe: exactly one does the real teardown, and
// both return without error.
func TestStopIsIdempotentUnderConcurrentCallers(t *testing.T) {
	t.Parallel()

	srv := newRunningTestServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	errs := make(chan error, 2)

	for i := 0; i < 2; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()
			errs <- srv.Stop(ctx)
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Stop returned an error: %v", err)
		}
	}

	if srv.Ready() {
		t.Fatal("server should report not-ready after Stop")
	}
}
