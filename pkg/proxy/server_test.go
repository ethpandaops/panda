package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sirupsen/logrus"

	simpleauth "github.com/ethpandaops/panda/pkg/auth"
)

func TestRegisterRoutesMatchesClickHouseSubpaths(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		ClickHouse: []ClickHouseClusterConfig{
			{
				Name:     "xatu",
				Host:     "example.com",
				Port:     8123,
				Username: "user",
				Password: "pass",
			},
		},
	}
	cfg.ApplyDefaults()

	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	if err != nil {
		t.Fatalf("newServer failed: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/clickhouse/query", nil)
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected clickhouse handler status %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestMetricsDatasourceLabelUsesConfiguredNamesOnly(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		ClickHouse: []ClickHouseClusterConfig{
			{Name: "xatu", Host: "example.com", Port: 8123, Username: "user", Password: "pass"},
		},
		Prometheus: []PrometheusInstanceConfig{
			{Name: "prod", URL: "https://prom.example.com"},
		},
	}
	cfg.ApplyDefaults()

	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	if err != nil {
		t.Fatalf("newServer failed: %v", err)
	}

	if got := srv.metricsDatasourceLabel("clickhouse", "xatu"); got != "xatu" {
		t.Fatalf("expected configured clickhouse datasource, got %q", got)
	}

	if got := srv.metricsDatasourceLabel("clickhouse", "attacker-"+t.Name()); got != "unknown" {
		t.Fatalf("expected unknown label for unconfigured datasource, got %q", got)
	}

	if got := srv.metricsDatasourceLabel("prometheus", ""); got != "default" {
		t.Fatalf("expected default label for empty datasource, got %q", got)
	}
}

func TestBrandingEndpointReturnsConfigWhenSet(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		Auth: AuthConfig{
			Mode: AuthModeNone,
			SuccessPage: &simpleauth.SuccessPageConfig{
				Default: &simpleauth.SuccessPageDisplay{
					Tagline: "Welcome to panda!",
				},
			},
		},
		ClickHouse: []ClickHouseClusterConfig{
			{Name: "xatu", Host: "example.com", Port: 8123, Username: "user", Password: "pass"},
		},
	}
	cfg.ApplyDefaults()

	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	if err != nil {
		t.Fatalf("newServer failed: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/branding", nil)
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var got simpleauth.SuccessPageConfig
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	if got.Default == nil || got.Default.Tagline != "Welcome to panda!" {
		t.Fatalf("unexpected default tagline: %+v", got.Default)
	}
}

func TestBrandingEndpointReturns204WhenNotConfigured(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		ClickHouse: []ClickHouseClusterConfig{
			{Name: "xatu", Host: "example.com", Port: 8123, Username: "user", Password: "pass"},
		},
	}
	cfg.ApplyDefaults()

	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	if err != nil {
		t.Fatalf("newServer failed: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/branding", nil)
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
}
