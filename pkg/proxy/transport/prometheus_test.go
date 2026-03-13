package transport

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPrometheusHandlerRetainsBrokenInstanceMarker(t *testing.T) {
	t.Parallel()

	handler := NewPrometheusHandler(testLogger(), []PrometheusConfig{
		{Name: "healthy", URL: "https://prom.example.com/prom"},
		{Name: "broken", URL: "://invalid"},
	})

	if handler.instances["healthy"] == nil {
		t.Fatal("healthy instance = nil, want configured instance")
	}
	if handler.instances["broken"] != nil {
		t.Fatalf("broken instance = %#v, want nil", handler.instances["broken"])
	}

	req := httptest.NewRequest(http.MethodGet, "http://proxy.test/prometheus", nil)
	req.Header.Set(DatasourceHeader, "broken")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}
