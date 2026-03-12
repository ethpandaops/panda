package transport

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLokiHandlerServeHTTPTargetsConfiguredBasePath(t *testing.T) {
	t.Parallel()

	handler, err := NewLokiHandler(testLogger(), []LokiConfig{{
		Name:     "logs",
		URL:      " https://logs.example.com/loki/api ",
		Username: "user",
		Password: "pass",
	}})
	if err != nil {
		t.Fatalf("NewLokiHandler() error = %v", err)
	}

	instance := handler.instances["logs"]
	if instance == nil {
		t.Fatal("instance = nil, want configured instance")
	}

	instance.proxy.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Scheme != "https" || req.URL.Host != "logs.example.com" {
			t.Fatalf("upstream target = %s://%s, want https://logs.example.com", req.URL.Scheme, req.URL.Host)
		}
		if req.URL.Path != "/loki/api/labels" {
			t.Fatalf("request path = %q, want %q", req.URL.Path, "/loki/api/labels")
		}

		return httpResponse(http.StatusOK, "labels", nil), nil
	})

	req := httptest.NewRequest(http.MethodGet, "http://proxy.test/loki/labels", nil)
	req.Header.Set(DatasourceHeader, "logs")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); got != "labels" {
		t.Fatalf("body = %q, want %q", got, "labels")
	}
}
