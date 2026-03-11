package proxyserver

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sirupsen/logrus"
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

func TestNewServerRejectsInvalidLokiConfig(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		Loki: []LokiInstanceConfig{
			{
				Name: "broken",
				URL:  "not-a-url",
			},
		},
	}
	cfg.ApplyDefaults()

	_, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	if err == nil {
		t.Fatal("expected newServer to fail")
	}
}
