package proxy

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	simpleauth "github.com/ethpandaops/panda/pkg/auth"
	"github.com/ethpandaops/panda/pkg/proxy/handlers"
)

func TestRegisterRoutesMatchesClickHouseSubpaths(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		ClickHouse: []ClickHouseClusterConfig{
			{
				BaseDatasourceConfig: BaseDatasourceConfig{Name: "clickhouse-raw"},
				Host:                 "example.com",
				Port:                 8123,
				Username:             "user",
				Password:             "pass",
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

func TestServerConfigAllowsAutodiscoverOnly(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		ClickHouse: []ClickHouseClusterConfig{
			{
				BaseDatasourceConfig: BaseDatasourceConfig{
					Name:        "local-kurtosis",
					Description: "Local OTel datasource",
				},
				Host:         "127.0.0.1",
				Port:         18123,
				Database:     "otel",
				Autodiscover: true,
			},
		},
	}
	cfg.ApplyDefaults()

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}

	if got := cfg.ClickHouse[0].AutodiscoverInterval; got != defaultAutodiscoverInterval {
		t.Fatalf("Autodiscover interval = %v, want 10s", got)
	}

	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	if err != nil {
		t.Fatalf("newServer failed: %v", err)
	}

	if srv.clickhouseHandler == nil {
		t.Fatal("expected clickhouse handler for autodiscover-only config")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/clickhouse/query", nil)
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected clickhouse route status %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestServerConfigRejectsAutodiscoverMissingDatabase(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		ClickHouse: []ClickHouseClusterConfig{
			{
				BaseDatasourceConfig: BaseDatasourceConfig{Name: "local-kurtosis"},
				Host:                 "127.0.0.1",
				Port:                 18123,
				Autodiscover:         true,
			},
		},
	}
	cfg.ApplyDefaults()

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want missing database error")
	}
	if !strings.Contains(err.Error(), "clickhouse[0].database is required when autodiscover is enabled") {
		t.Fatalf("Validate() error = %q, want missing database error", err)
	}
}

func TestServerConfigRejectsNegativeAutodiscoverInterval(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		ClickHouse: []ClickHouseClusterConfig{
			{
				BaseDatasourceConfig: BaseDatasourceConfig{Name: "local-kurtosis"},
				Host:                 "127.0.0.1",
				Port:                 18123,
				Database:             "otel",
				Autodiscover:         true,
				AutodiscoverInterval: -1 * time.Second,
			},
		},
	}
	cfg.ApplyDefaults()

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want negative interval error")
	}
	if !strings.Contains(err.Error(), "clickhouse[0].autodiscover_interval cannot be negative") {
		t.Fatalf("Validate() error = %q, want negative interval error", err)
	}
}

func TestAutodiscoverCannotManageStaticClickHouseDatasource(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		ClickHouse: []ClickHouseClusterConfig{
			{
				BaseDatasourceConfig: BaseDatasourceConfig{
					Name:        "local-kurtosis",
					Description: "Static datasource",
				},
				Host:     "static.example",
				Port:     8123,
				Database: "staticdb",
			},
			{
				BaseDatasourceConfig: BaseDatasourceConfig{Name: "local-kurtosis"},
				Host:                 "127.0.0.1",
				Port:                 18123,
				Database:             "otel",
				Autodiscover:         true,
			},
		},
	}
	cfg.ApplyDefaults()

	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	if err != nil {
		t.Fatalf("newServer failed: %v", err)
	}

	if added := srv.addAutodiscoveredClickHouseCluster(handlers.ClickHouseConfig{
		Name:        "local-kurtosis",
		Description: "Dynamic datasource",
		Host:        "dynamic.example",
		Port:        18123,
		Database:    "otel",
	}); added {
		t.Fatal("expected autodiscover add to be skipped for static datasource name")
	}

	cfgAfterAdd, ok := srv.clickhouseHandler.ClusterConfig("local-kurtosis")
	if !ok {
		t.Fatal("expected static local-kurtosis cluster to remain")
	}
	if cfgAfterAdd.Description != "Static datasource" || cfgAfterAdd.Database != "staticdb" {
		t.Fatalf("cluster after autodiscover add = %#v, want unchanged static config", cfgAfterAdd)
	}

	srv.removeAutodiscoveredClickHouseCluster("local-kurtosis")

	cfgAfterRemove, ok := srv.clickhouseHandler.ClusterConfig("local-kurtosis")
	if !ok {
		t.Fatal("expected static local-kurtosis cluster to remain after dynamic remove")
	}
	if cfgAfterRemove.Description != "Static datasource" || cfgAfterRemove.Database != "staticdb" {
		t.Fatalf("cluster after autodiscover remove = %#v, want unchanged static config", cfgAfterRemove)
	}
}

func TestAutodiscoverReconcilesClickHouseDatasource(t *testing.T) {
	t.Parallel()

	pingOK := true
	databaseExists := false
	fakeClickHouse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ping":
			if !pingOK {
				_, _ = w.Write([]byte("Nope."))
				return
			}

			_, _ = w.Write([]byte("Ok."))
		case "/":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("reading request body: %v", err)
			}

			query := string(body)
			if !strings.Contains(query, "system.databases") {
				t.Fatalf("database probe query = %q, want system.databases", query)
			}
			if !strings.Contains(query, "'otel'") {
				t.Fatalf("database probe query = %q, want quoted otel database", query)
			}

			if !databaseExists {
				return
			}

			_, _ = w.Write([]byte("1\n"))
		default:
			t.Fatalf("unexpected autodiscover request path %q", r.URL.Path)
		}
	}))
	defer fakeClickHouse.Close()

	wantHost, wantPort := clickHouseAutodiscoverTestAddress(t, fakeClickHouse)

	cfg := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		ClickHouse: []ClickHouseClusterConfig{
			{
				BaseDatasourceConfig: BaseDatasourceConfig{
					Name:        "local-kurtosis",
					Description: "Custom local OTel datasource",
				},
				Host:                 wantHost,
				Port:                 wantPort,
				Database:             "otel",
				Autodiscover:         true,
				AutodiscoverInterval: time.Hour,
			},
		},
	}
	cfg.ApplyDefaults()

	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	if err != nil {
		t.Fatalf("newServer failed: %v", err)
	}

	present := false
	entry := cfg.ClickHouse[0]

	srv.reconcileClickHouseAutodiscover(t.Context(), entry, &present)
	if present {
		t.Fatal("expected datasource to remain absent when database is missing")
	}
	if srv.clickhouseHandler.HasCluster("local-kurtosis") {
		t.Fatal("expected local-kurtosis cluster to be absent")
	}

	databaseExists = true
	srv.reconcileClickHouseAutodiscover(t.Context(), entry, &present)
	if !present {
		t.Fatal("expected datasource to become present")
	}
	if !srv.clickhouseHandler.HasCluster("local-kurtosis") {
		t.Fatal("expected local-kurtosis cluster to be present")
	}
	clusterCfg, ok := srv.clickhouseHandler.ClusterConfig("local-kurtosis")
	if !ok {
		t.Fatal("expected local-kurtosis cluster config to exist")
	}
	if clusterCfg.Host != wantHost || clusterCfg.Port != wantPort || clusterCfg.Database != "otel" ||
		clusterCfg.Description != "Custom local OTel datasource" {
		t.Fatalf("cluster config = %#v, want host=%q port=%d database=otel description=custom", clusterCfg, wantHost, wantPort)
	}

	info := srv.ClickHouseDatasourceInfo()
	if len(info) != 1 {
		t.Fatalf("ClickHouseDatasourceInfo() length = %d, want 1", len(info))
	}
	if info[0].Name != "local-kurtosis" {
		t.Fatalf("ClickHouseDatasourceInfo()[0].Name = %q, want local-kurtosis", info[0].Name)
	}
	if info[0].Description != "Custom local OTel datasource" {
		t.Fatalf("ClickHouseDatasourceInfo()[0].Description = %q, want custom description", info[0].Description)
	}
	if got := info[0].Metadata["database"]; got != "otel" {
		t.Fatalf("ClickHouseDatasourceInfo()[0].Metadata[database] = %q, want otel", got)
	}

	var datasources DatasourcesResponse
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/datasources", nil)
	srv.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/datasources status = %d, want %d", rec.Code, http.StatusOK)
	}
	if err := json.NewDecoder(rec.Body).Decode(&datasources); err != nil {
		t.Fatalf("decoding /datasources response: %v", err)
	}
	if len(datasources.ClickHouse) != 1 || datasources.ClickHouse[0] != "local-kurtosis" {
		t.Fatalf("/datasources ClickHouse = %v, want [local-kurtosis]", datasources.ClickHouse)
	}
	if len(datasources.ClickHouseInfo) != 1 || datasources.ClickHouseInfo[0].Metadata["database"] != "otel" {
		t.Fatalf("/datasources ClickHouseInfo = %v, want local-kurtosis metadata database=otel", datasources.ClickHouseInfo)
	}
	if datasources.ClickHouseInfo[0].Description != "Custom local OTel datasource" {
		t.Fatalf("/datasources ClickHouseInfo[0].Description = %q, want custom description", datasources.ClickHouseInfo[0].Description)
	}

	pingOK = false
	srv.reconcileClickHouseAutodiscover(t.Context(), entry, &present)
	if present {
		t.Fatal("expected datasource to become absent")
	}
	if srv.clickhouseHandler.HasCluster("local-kurtosis") {
		t.Fatal("expected local-kurtosis cluster to be removed")
	}
	if got := srv.ClickHouseDatasourceInfo(); len(got) != 0 {
		t.Fatalf("ClickHouseDatasourceInfo() after removal = %v, want empty", got)
	}

	pingOK = true
	srv.reconcileClickHouseAutodiscover(t.Context(), entry, &present)
	if !present {
		t.Fatal("expected datasource to become present again")
	}
	if !srv.clickhouseHandler.HasCluster("local-kurtosis") {
		t.Fatal("expected local-kurtosis cluster to be re-added")
	}
}

func clickHouseAutodiscoverTestAddress(t *testing.T, srv *httptest.Server) (string, int) {
	t.Helper()

	parsed, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parsing fake ClickHouse URL: %v", err)
	}

	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatalf("parsing fake ClickHouse port: %v", err)
	}

	return parsed.Hostname(), port
}

func TestURLFromEphemeralListenerAddr(t *testing.T) {
	t.Parallel()

	if !listenPortIsEphemeral("127.0.0.1:0") {
		t.Fatalf("listenPortIsEphemeral(127.0.0.1:0) = false, want true")
	}
	if listenPortIsEphemeral("127.0.0.1:18081") {
		t.Fatalf("listenPortIsEphemeral(127.0.0.1:18081) = true, want false")
	}

	addr := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 49321}
	if got := urlFromListenerAddr(addr, "http://localhost:0"); got != "http://127.0.0.1:49321" {
		t.Fatalf("urlFromListenerAddr() = %q, want actual listener URL", got)
	}
}

func TestMetricsDatasourceLabelUsesConfiguredNamesOnly(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		ClickHouse: []ClickHouseClusterConfig{
			{BaseDatasourceConfig: BaseDatasourceConfig{Name: "clickhouse-raw"}, Host: "example.com", Port: 8123, Username: "user", Password: "pass"},
		},
		Prometheus: []PrometheusInstanceConfig{
			{BaseDatasourceConfig: BaseDatasourceConfig{Name: "prod"}, URL: "https://prom.example.com"},
		},
	}
	cfg.ApplyDefaults()

	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	if err != nil {
		t.Fatalf("newServer failed: %v", err)
	}

	if got := srv.metricsDatasourceLabel("clickhouse", "clickhouse-raw"); got != "clickhouse-raw" {
		t.Fatalf("expected configured clickhouse datasource, got %q", got)
	}

	if got := srv.metricsDatasourceLabel("clickhouse", "attacker-"+t.Name()); got != "unknown" {
		t.Fatalf("expected unknown label for unconfigured datasource, got %q", got)
	}

	if got := srv.metricsDatasourceLabel("prometheus", ""); got != "default" {
		t.Fatalf("expected default label for empty datasource, got %q", got)
	}
}

func TestAuthMetadataEndpointReturnsConfig(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		Auth: AuthConfig{
			Mode:      AuthModeOIDC,
			IssuerURL: "https://dex.example.com",
			ClientID:  "panda-proxy",
		},
		ClickHouse: []ClickHouseClusterConfig{
			{BaseDatasourceConfig: BaseDatasourceConfig{Name: "clickhouse-raw"}, Host: "example.com", Port: 8123, Username: "user", Password: "pass"},
		},
	}
	cfg.ApplyDefaults()

	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	if err != nil {
		t.Fatalf("newServer failed: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/metadata", nil)
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var got AuthMetadataResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	if !got.Enabled {
		t.Fatal("expected enabled=true")
	}

	if got.Mode != "oidc" {
		t.Fatalf("expected mode=oidc, got %q", got.Mode)
	}

	if got.IssuerURL != "https://dex.example.com" {
		t.Fatalf("expected issuer_url=https://dex.example.com, got %q", got.IssuerURL)
	}

	if got.ClientID != "panda-proxy" {
		t.Fatalf("expected client_id=panda-proxy, got %q", got.ClientID)
	}
}

func TestAuthMetadataEndpointNoAuth(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		ClickHouse: []ClickHouseClusterConfig{
			{BaseDatasourceConfig: BaseDatasourceConfig{Name: "clickhouse-raw"}, Host: "example.com", Port: 8123, Username: "user", Password: "pass"},
		},
	}
	cfg.ApplyDefaults()

	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	if err != nil {
		t.Fatalf("newServer failed: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/metadata", nil)
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var got AuthMetadataResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	if got.Enabled {
		t.Fatal("expected enabled=false for none mode")
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
			{BaseDatasourceConfig: BaseDatasourceConfig{Name: "clickhouse-raw"}, Host: "example.com", Port: 8123, Username: "user", Password: "pass"},
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
			{BaseDatasourceConfig: BaseDatasourceConfig{Name: "clickhouse-raw"}, Host: "example.com", Port: 8123, Username: "user", Password: "pass"},
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
