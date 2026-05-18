package proxy

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ethpandaops/panda/pkg/proxy/handlers"
	"github.com/ethpandaops/panda/pkg/types"
)

func testConfig() ServerConfig {
	cfg := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		ClickHouse: []ClickHouseClusterConfig{
			{BaseDatasourceConfig: BaseDatasourceConfig{Name: "restricted", AllowedOrgs: []string{"ethpandaops"}}, Host: "example.com", Port: 8123, Username: "u", Password: "p"},
			{BaseDatasourceConfig: BaseDatasourceConfig{Name: "public"}, Host: "example.com", Port: 8123, Username: "u", Password: "p"},
		},
		Prometheus: []PrometheusInstanceConfig{
			{BaseDatasourceConfig: BaseDatasourceConfig{Name: "internal", AllowedOrgs: []string{"ethpandaops", "sigp"}}, URL: "https://prom.example.com"},
		},
		Loki: []LokiInstanceConfig{
			{BaseDatasourceConfig: BaseDatasourceConfig{Name: "logs"}, URL: "https://loki.example.com"},
		},
	}
	cfg.ApplyDefaults()

	return cfg
}

func requestWithProxyUser(method, path string, groups []string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	ctx := withAuthUser(req.Context(), &AuthUser{
		Subject:  "user1",
		Username: "testuser",
		Groups:   groups,
	})

	return req.WithContext(ctx)
}

// requestWithOAuthUser simulates an OAuth-authenticated user.
// In the real OAuth flow, Groups is populated from Orgs (see auth_simple.go line 339),
// so both auth paths are exercised via the proxy AuthUser context.
func requestWithOAuthUser(method, path string, orgs []string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	ctx := withAuthUser(req.Context(), &AuthUser{
		Subject:  "user1",
		Username: "testuser",
		Groups:   orgs,
	})

	return req.WithContext(ctx)
}

func TestAuthorizerMiddlewareAllowsMatchingOrg(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	require.NoError(t, err)

	// User in ethpandaops org accessing restricted clickhouse.
	rec := httptest.NewRecorder()
	req := requestWithProxyUser(http.MethodPost, "/clickhouse", []string{"ethpandaops"})
	req.Header.Set("X-Datasource", "restricted")
	srv.mux.ServeHTTP(rec, req)

	// Should reach the handler (400 = missing query, not 403).
	assert.NotEqual(t, http.StatusForbidden, rec.Code, "should not be forbidden")
}

func TestAuthorizerMiddlewareDeniesNonMatchingOrg(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	require.NoError(t, err)

	// User in wrong org accessing restricted clickhouse.
	rec := httptest.NewRecorder()
	req := requestWithProxyUser(http.MethodPost, "/clickhouse", []string{"other-org"})
	req.Header.Set("X-Datasource", "restricted")
	srv.mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestAuthorizerMiddlewareAllowsUnrestrictedDatasource(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	require.NoError(t, err)

	// User in any org accessing public clickhouse (no allowed_orgs).
	rec := httptest.NewRecorder()
	req := requestWithProxyUser(http.MethodPost, "/clickhouse", []string{"random-org"})
	req.Header.Set("X-Datasource", "public")
	srv.mux.ServeHTTP(rec, req)

	assert.NotEqual(t, http.StatusForbidden, rec.Code, "unrestricted datasource should be accessible")
}

func TestAuthorizerMiddlewareAllowsNoAuthUser(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	require.NoError(t, err)

	// No auth user (none mode) — should pass through.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/clickhouse", nil)
	req.Header.Set("X-Datasource", "restricted")
	srv.mux.ServeHTTP(rec, req)

	assert.NotEqual(t, http.StatusForbidden, rec.Code, "no auth user should pass through")
}

func TestAuthorizerMiddlewareOAuthMode(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	require.NoError(t, err)

	// OAuth user with matching org.
	rec := httptest.NewRecorder()
	req := requestWithOAuthUser(http.MethodPost, "/clickhouse", []string{"ethpandaops"})
	req.Header.Set("X-Datasource", "restricted")
	srv.mux.ServeHTTP(rec, req)

	assert.NotEqual(t, http.StatusForbidden, rec.Code, "oauth user with matching org should pass")

	// OAuth user without matching org.
	rec = httptest.NewRecorder()
	req = requestWithOAuthUser(http.MethodPost, "/clickhouse", []string{"wrong-org"})
	req.Header.Set("X-Datasource", "restricted")
	srv.mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestAuthorizerFilterDatasources(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	authorizer := NewAuthorizer(logrus.New(), cfg)

	const testEmbeddingModel = "test-embed-model"

	resp := DatasourcesResponse{
		ClickHouse:         []string{"restricted", "public"},
		ClickHouseInfo:     []types.DatasourceInfo{{Type: "clickhouse", Name: "restricted"}, {Type: "clickhouse", Name: "public"}},
		Prometheus:         []string{"internal"},
		PrometheusInfo:     []types.DatasourceInfo{{Type: "prometheus", Name: "internal"}},
		Loki:               []string{"logs"},
		LokiInfo:           []types.DatasourceInfo{{Type: "loki", Name: "logs"}},
		EmbeddingAvailable: true,
		EmbeddingModel:     testEmbeddingModel,
	}

	// Embedding is infrastructure metadata, not a per-user datasource — it must
	// survive filtering unconditionally regardless of org membership.
	assertEmbeddingPreserved := func(t *testing.T, filtered DatasourcesResponse) {
		t.Helper()
		assert.True(t, filtered.EmbeddingAvailable)
		assert.Equal(t, testEmbeddingModel, filtered.EmbeddingModel)
	}

	// User in ethpandaops — should see everything.
	ctx := withAuthUser(context.Background(), &AuthUser{Groups: []string{"ethpandaops"}})
	filtered := authorizer.FilterDatasources(ctx, resp)
	assert.Equal(t, []string{"restricted", "public"}, filtered.ClickHouse)
	assert.Equal(t, []string{"internal"}, filtered.Prometheus)
	assert.Equal(t, []string{"logs"}, filtered.Loki)
	assertEmbeddingPreserved(t, filtered)

	// User in sigp — should see public clickhouse + internal prometheus + logs.
	ctx = withAuthUser(context.Background(), &AuthUser{Groups: []string{"sigp"}})
	filtered = authorizer.FilterDatasources(ctx, resp)
	assert.Equal(t, []string{"public"}, filtered.ClickHouse)
	assert.Equal(t, []string{"internal"}, filtered.Prometheus)
	assert.Equal(t, []string{"logs"}, filtered.Loki)
	assertEmbeddingPreserved(t, filtered)

	// User in unknown org — only unrestricted datasources.
	ctx = withAuthUser(context.Background(), &AuthUser{Groups: []string{"unknown"}})
	filtered = authorizer.FilterDatasources(ctx, resp)
	assert.Equal(t, []string{"public"}, filtered.ClickHouse)
	assert.Empty(t, filtered.Prometheus)
	assert.Equal(t, []string{"logs"}, filtered.Loki)
	assertEmbeddingPreserved(t, filtered)

	// No auth user — return everything.
	filtered = authorizer.FilterDatasources(context.Background(), resp)
	assert.Equal(t, resp, filtered)
}

func TestAuthorizerFilterDatasourcesEndpoint(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	require.NoError(t, err)

	// User in ethpandaops — should see all datasources.
	rec := httptest.NewRecorder()
	req := requestWithProxyUser(http.MethodGet, "/datasources", []string{"ethpandaops"})
	srv.mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp DatasourcesResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Contains(t, resp.ClickHouse, "restricted")
	assert.Contains(t, resp.ClickHouse, "public")

	// User in unknown org — should only see unrestricted datasources.
	rec = httptest.NewRecorder()
	req = requestWithProxyUser(http.MethodGet, "/datasources", []string{"unknown"})
	srv.mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.NotContains(t, resp.ClickHouse, "restricted")
	assert.Contains(t, resp.ClickHouse, "public")
}

func TestAuthorizerSelectsDatasourceVariants(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		ClickHouse: []ClickHouseClusterConfig{
			{
				BaseDatasourceConfig: BaseDatasourceConfig{Name: "clickhouse-raw"},
				Variants: []ClickHouseClusterVariantConfig{
					{
						DatasourceVariantConfig: DatasourceVariantConfig{AllowedOrgs: []string{"ethpandaops:Core"}},
						Host:                    "clickhouse.internal.example.com",
						Database:                "internal",
						Username:                "pandaproxy_internal",
						Password:                "secret",
					},
					{
						Host:     "clickhouse.external.example.com",
						Database: "external",
						Username: "pandaproxy_external",
						Password: "secret",
					},
				},
			},
		},
		Prometheus: []PrometheusInstanceConfig{
			{
				BaseDatasourceConfig: BaseDatasourceConfig{Name: "prometheus-main"},
				Variants: []PrometheusInstanceVariantConfig{
					{
						DatasourceVariantConfig: DatasourceVariantConfig{AllowedOrgs: []string{"ethpandaops:Core"}},
						URL:                     "https://prom-internal.example.com",
						Username:                "internal",
					},
					{
						URL:      "https://prom-external.example.com",
						Username: "external",
					},
				},
			},
		},
		Loki: []LokiInstanceConfig{
			{
				BaseDatasourceConfig: BaseDatasourceConfig{Name: "loki-main"},
				Variants: []LokiInstanceVariantConfig{
					{
						DatasourceVariantConfig: DatasourceVariantConfig{AllowedOrgs: []string{"ethpandaops:Core"}},
						URL:                     "https://loki-internal.example.com",
						Username:                "internal",
					},
					{
						URL:      "https://loki-external.example.com",
						Username: "external",
					},
				},
			},
		},
	}
	cfg.ApplyDefaults()
	require.NoError(t, cfg.Validate())

	authorizer := NewAuthorizer(logrus.New(), cfg)
	chConfigs, promConfigs, lokiConfigs, _ := cfg.ToHandlerConfigs()

	coreCtx := withAuthUser(context.Background(), &AuthUser{Groups: []string{"ethpandaops:Core"}})
	route, ok := authorizer.RouteName(coreCtx, "clickhouse", "clickhouse-raw")
	require.True(t, ok)
	assert.Equal(t, "pandaproxy_internal", clickHouseUsernamesByRoute(chConfigs)[route])

	route, ok = authorizer.RouteName(coreCtx, "prometheus", "prometheus-main")
	require.True(t, ok)
	assert.Equal(t, "internal", prometheusUsernamesByRoute(promConfigs)[route])

	route, ok = authorizer.RouteName(coreCtx, "loki", "loki-main")
	require.True(t, ok)
	assert.Equal(t, "internal", lokiUsernamesByRoute(lokiConfigs)[route])

	nonCoreCtx := withAuthUser(context.Background(), &AuthUser{Groups: []string{"other-org"}})
	route, ok = authorizer.RouteName(nonCoreCtx, "clickhouse", "clickhouse-raw")
	require.True(t, ok)
	assert.Equal(t, "pandaproxy_external", clickHouseUsernamesByRoute(chConfigs)[route])

	route, ok = authorizer.RouteName(nonCoreCtx, "prometheus", "prometheus-main")
	require.True(t, ok)
	assert.Equal(t, "external", prometheusUsernamesByRoute(promConfigs)[route])

	route, ok = authorizer.RouteName(nonCoreCtx, "loki", "loki-main")
	require.True(t, ok)
	assert.Equal(t, "external", lokiUsernamesByRoute(lokiConfigs)[route])

	resp := DatasourcesResponse{
		ClickHouse:     []string{"clickhouse-raw"},
		ClickHouseInfo: []types.DatasourceInfo{{Type: "clickhouse", Name: "clickhouse-raw"}},
		Prometheus:     []string{"prometheus-main"},
		PrometheusInfo: []types.DatasourceInfo{{Type: "prometheus", Name: "prometheus-main"}},
		Loki:           []string{"loki-main"},
		LokiInfo:       []types.DatasourceInfo{{Type: "loki", Name: "loki-main"}},
	}

	filtered := authorizer.FilterDatasources(coreCtx, resp)
	assert.Equal(t, []string{"clickhouse-raw"}, filtered.ClickHouse)
	require.Len(t, filtered.ClickHouseInfo, 1)
	assert.Equal(t, "internal", filtered.ClickHouseInfo[0].Metadata["database"])
	require.Len(t, filtered.PrometheusInfo, 1)
	assert.Equal(t, "https://prom-internal.example.com", filtered.PrometheusInfo[0].Metadata["url"])
	require.Len(t, filtered.LokiInfo, 1)
	assert.Equal(t, "https://loki-internal.example.com", filtered.LokiInfo[0].Metadata["url"])

	filtered = authorizer.FilterDatasources(nonCoreCtx, resp)
	assert.Equal(t, []string{"clickhouse-raw"}, filtered.ClickHouse)
	require.Len(t, filtered.ClickHouseInfo, 1)
	assert.Equal(t, "external", filtered.ClickHouseInfo[0].Metadata["database"])
	require.Len(t, filtered.PrometheusInfo, 1)
	assert.Equal(t, "https://prom-external.example.com", filtered.PrometheusInfo[0].Metadata["url"])
	require.Len(t, filtered.LokiInfo, 1)
	assert.Equal(t, "https://loki-external.example.com", filtered.LokiInfo[0].Metadata["url"])
}

func TestAuthorizerDeniesWhenNoDatasourceVariantMatches(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		ClickHouse: []ClickHouseClusterConfig{
			{
				BaseDatasourceConfig: BaseDatasourceConfig{Name: "clickhouse-raw"},
				Variants: []ClickHouseClusterVariantConfig{
					{
						DatasourceVariantConfig: DatasourceVariantConfig{AllowedOrgs: []string{"ethpandaops:Core"}},
						Host:                    "clickhouse.internal.example.com",
						Username:                "pandaproxy_internal",
						Password:                "secret",
					},
				},
			},
		},
	}
	cfg.ApplyDefaults()
	require.NoError(t, cfg.Validate())

	authorizer := NewAuthorizer(logrus.New(), cfg)
	ctx := withAuthUser(context.Background(), &AuthUser{Groups: []string{"other-org"}})

	assert.False(t, authorizer.isAllowed(ctx, "clickhouse", "clickhouse-raw"))

	_, ok := authorizer.RouteName(ctx, "clickhouse", "clickhouse-raw")
	assert.False(t, ok)

	filtered := authorizer.FilterDatasources(ctx, DatasourcesResponse{
		ClickHouse:     []string{"clickhouse-raw"},
		ClickHouseInfo: []types.DatasourceInfo{{Type: "clickhouse", Name: "clickhouse-raw"}},
	})
	assert.Empty(t, filtered.ClickHouse)
	assert.Empty(t, filtered.ClickHouseInfo)
}

func TestLegacyTopLevelDatasourceConfigStillWorks(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		ClickHouse: []ClickHouseClusterConfig{
			{
				BaseDatasourceConfig: BaseDatasourceConfig{Name: "restricted", AllowedOrgs: []string{"ethpandaops:Core"}},
				Host:                 "clickhouse.example.com",
				Username:             "pandaproxy",
				Password:             "secret",
			},
		},
	}
	cfg.ApplyDefaults()
	require.NoError(t, cfg.Validate())

	authorizer := NewAuthorizer(logrus.New(), cfg)

	coreCtx := withAuthUser(context.Background(), &AuthUser{Groups: []string{"ethpandaops:Core"}})
	route, ok := authorizer.RouteName(coreCtx, "clickhouse", "restricted")
	require.True(t, ok)
	assert.Equal(t, "restricted", route)
	assert.True(t, authorizer.isAllowed(coreCtx, "clickhouse", "restricted"))

	otherCtx := withAuthUser(context.Background(), &AuthUser{Groups: []string{"other-org"}})
	assert.False(t, authorizer.isAllowed(otherCtx, "clickhouse", "restricted"))

	chConfigs, _, _, _ := cfg.ToHandlerConfigs()
	require.Len(t, chConfigs, 1)
	assert.Equal(t, "restricted", chConfigs[0].Name)
	assert.Empty(t, chConfigs[0].RouteName)
	assert.Equal(t, "pandaproxy", chConfigs[0].Username)
}

func TestDatasourceVariantsRejectTopLevelBackendFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     ServerConfig
		wantErr string
	}{
		{
			name: "clickhouse",
			cfg: ServerConfig{
				Auth: AuthConfig{Mode: AuthModeNone},
				ClickHouse: []ClickHouseClusterConfig{
					{
						BaseDatasourceConfig: BaseDatasourceConfig{Name: "mixed"},
						Host:                 "top-level.example.com",
						Variants: []ClickHouseClusterVariantConfig{
							{Host: "variant.example.com", Username: "variant", Password: "secret"},
						},
					},
				},
			},
			wantErr: "clickhouse[0] cannot mix variants with top-level backend fields: host",
		},
		{
			name: "prometheus",
			cfg: ServerConfig{
				Auth: AuthConfig{Mode: AuthModeNone},
				Prometheus: []PrometheusInstanceConfig{
					{
						BaseDatasourceConfig: BaseDatasourceConfig{Name: "mixed"},
						URL:                  "https://top-level.example.com",
						Variants: []PrometheusInstanceVariantConfig{
							{URL: "https://variant.example.com"},
						},
					},
				},
			},
			wantErr: "prometheus[0] cannot mix variants with top-level backend fields: url",
		},
		{
			name: "loki",
			cfg: ServerConfig{
				Auth: AuthConfig{Mode: AuthModeNone},
				Loki: []LokiInstanceConfig{
					{
						BaseDatasourceConfig: BaseDatasourceConfig{Name: "mixed"},
						URL:                  "https://top-level.example.com",
						Variants: []LokiInstanceVariantConfig{
							{URL: "https://variant.example.com"},
						},
					},
				},
			},
			wantErr: "loki[0] cannot mix variants with top-level backend fields: url",
		},
		{
			name: "top-level allowed_orgs",
			cfg: ServerConfig{
				Auth: AuthConfig{Mode: AuthModeNone},
				Prometheus: []PrometheusInstanceConfig{
					{
						BaseDatasourceConfig: BaseDatasourceConfig{Name: "mixed", AllowedOrgs: []string{"ethpandaops:Core"}},
						Variants: []PrometheusInstanceVariantConfig{
							{URL: "https://variant.example.com"},
						},
					},
				},
			},
			wantErr: "prometheus[0].allowed_orgs cannot be set with variants; set allowed_orgs on each variant",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tt.cfg.ApplyDefaults()
			err := tt.cfg.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestDatasourceVariantsRejectExplicitZeroTopLevelBackendFieldsFromYAML(t *testing.T) {
	t.Parallel()

	var cfg ServerConfig
	require.NoError(t, yaml.Unmarshal([]byte(`
clickhouse:
  - name: mixed
    secure: false
    variants:
      - host: variant.example.com
        username: variant
        password: secret
`), &cfg))

	cfg.ApplyDefaults()
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "clickhouse[0] cannot mix variants with top-level backend fields: secure")
}

func TestClickHouseVariantRoutesToSelectedBackend(t *testing.T) {
	t.Parallel()

	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, _, _ := r.BasicAuth()
		_, _ = w.Write([]byte("internal:" + username))
	}))
	defer internal.Close()

	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, _, _ := r.BasicAuth()
		_, _ = w.Write([]byte("external:" + username))
	}))
	defer external.Close()

	cfg := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		ClickHouse: []ClickHouseClusterConfig{
			{
				BaseDatasourceConfig: BaseDatasourceConfig{Name: "clickhouse-raw"},
				Variants: []ClickHouseClusterVariantConfig{
					clickHouseVariantFromServer(t, internal.URL, []string{"ethpandaops:Core"}, "pandaproxy_internal"),
					clickHouseVariantFromServer(t, external.URL, nil, "pandaproxy_external"),
				},
			},
		},
	}
	cfg.ApplyDefaults()

	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	req := requestWithProxyUser(http.MethodGet, "/clickhouse", []string{"ethpandaops:Core"})
	req.Header.Set("X-Datasource", "clickhouse-raw")
	srv.mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "internal:pandaproxy_internal", rec.Body.String())

	rec = httptest.NewRecorder()
	req = requestWithProxyUser(http.MethodGet, "/clickhouse", []string{"other-org"})
	req.Header.Set("X-Datasource", "clickhouse-raw")
	srv.mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "external:pandaproxy_external", rec.Body.String())
}

func TestAuthorizerEthnode(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		ClickHouse: []ClickHouseClusterConfig{
			{BaseDatasourceConfig: BaseDatasourceConfig{Name: "ch"}, Host: "example.com", Port: 8123, Username: "u", Password: "p"},
		},
		EthNode: &EthNodeInstanceConfig{
			BaseDatasourceConfig: BaseDatasourceConfig{AllowedOrgs: []string{"ethpandaops"}},
			Username:             "u",
			Password:             "p",
		},
	}
	cfg.ApplyDefaults()

	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	require.NoError(t, err)

	// User in ethpandaops — should pass through to handler.
	rec := httptest.NewRecorder()
	req := requestWithProxyUser(http.MethodGet, "/beacon/mainnet/lighthouse/eth/v1/node/version", []string{"ethpandaops"})
	srv.mux.ServeHTTP(rec, req)
	assert.NotEqual(t, http.StatusForbidden, rec.Code)

	// User in wrong org — should get 403.
	rec = httptest.NewRecorder()
	req = requestWithProxyUser(http.MethodGet, "/beacon/mainnet/lighthouse/eth/v1/node/version", []string{"other"})
	srv.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func clickHouseUsernamesByRoute(configs []handlers.ClickHouseConfig) map[string]string {
	result := make(map[string]string, len(configs))
	for _, cfg := range configs {
		result[testRouteName(cfg.Name, cfg.RouteName)] = cfg.Username
	}

	return result
}

func prometheusUsernamesByRoute(configs []handlers.PrometheusConfig) map[string]string {
	result := make(map[string]string, len(configs))
	for _, cfg := range configs {
		result[testRouteName(cfg.Name, cfg.RouteName)] = cfg.Username
	}

	return result
}

func lokiUsernamesByRoute(configs []handlers.LokiConfig) map[string]string {
	result := make(map[string]string, len(configs))
	for _, cfg := range configs {
		result[testRouteName(cfg.Name, cfg.RouteName)] = cfg.Username
	}

	return result
}

func testRouteName(name, routeName string) string {
	if routeName != "" {
		return routeName
	}

	return name
}

func clickHouseVariantFromServer(t *testing.T, rawURL string, allowedOrgs []string, username string) ClickHouseClusterVariantConfig {
	t.Helper()

	parsedURL, err := url.Parse(rawURL)
	require.NoError(t, err)

	host, portValue, err := net.SplitHostPort(parsedURL.Host)
	require.NoError(t, err)

	port, err := strconv.Atoi(portValue)
	require.NoError(t, err)

	return ClickHouseClusterVariantConfig{
		DatasourceVariantConfig: DatasourceVariantConfig{AllowedOrgs: allowedOrgs},
		Host:                    host,
		Port:                    port,
		Username:                username,
		Password:                "secret",
		Secure:                  parsedURL.Scheme == "https",
	}
}
