package transport

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEthNodeHandlerCreatesExecutionProxy(t *testing.T) {
	t.Parallel()

	handler := NewEthNodeHandler(testLogger(), EthNodeConfig{
		Username: "user",
		Password: "pass",
	})

	req := httptest.NewRequest(http.MethodPost, "http://proxy.test/execution/holesky/archive", nil)
	req.Header.Set("Authorization", "Bearer sandbox-token")

	proxy := handler.getOrCreateProxy("rpc-archive.srv.holesky.ethpandaops.io")
	proxy.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Scheme != "https" || req.URL.Host != "rpc-archive.srv.holesky.ethpandaops.io" {
			t.Fatalf("upstream target = %s://%s, want https://rpc-archive.srv.holesky.ethpandaops.io", req.URL.Scheme, req.URL.Host)
		}
		if req.URL.Path != "/" {
			t.Fatalf("request path = %q, want /", req.URL.Path)
		}
		if req.Host != "rpc-archive.srv.holesky.ethpandaops.io" {
			t.Fatalf("request host = %q, want rpc-archive.srv.holesky.ethpandaops.io", req.Host)
		}
		if got := req.Header.Get("Authorization"); got == "" || strings.HasPrefix(got, "Bearer ") {
			t.Fatalf("authorization header = %q, want basic auth only", got)
		}
		username, password, ok := req.BasicAuth()
		if !ok || username != "user" || password != "pass" {
			t.Fatalf("BasicAuth() = (%q, %q, %v), want (%q, %q, true)", username, password, ok, "user", "pass")
		}

		return httpResponse(http.StatusAccepted, "ok", nil), nil
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	if got := len(handler.proxes); got != 1 {
		t.Fatalf("cached proxies = %d, want 1", got)
	}
}
