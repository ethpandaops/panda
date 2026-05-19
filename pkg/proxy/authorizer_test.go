package proxy

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
					{AllowedOrgs: []string{"ethpandaops:Core"}, Host: "internal.example.com", Database: "internal", Username: "internal", Password: "secret"},
					{Host: "external.example.com", Database: "external", Username: "external", Password: "secret"},
				},
			},
		},
	}
	cfg.ApplyDefaults()
	require.NoError(t, cfg.Validate())

	authorizer := NewAuthorizer(logrus.New(), cfg)
	chConfigs, _, _, _ := cfg.ToHandlerConfigs()
	resp := DatasourcesResponse{
		ClickHouse:     []string{"clickhouse-raw"},
		ClickHouseInfo: []types.DatasourceInfo{{Type: "clickhouse", Name: "clickhouse-raw"}},
	}

	coreCtx := withAuthUser(context.Background(), &AuthUser{Groups: []string{"ethpandaops:Core"}})
	filtered := authorizer.FilterDatasources(coreCtx, resp)
	assert.Equal(t, []string{"clickhouse-raw"}, filtered.ClickHouse)
	require.Len(t, filtered.ClickHouseInfo, 1)
	assert.Equal(t, "internal", filtered.ClickHouseInfo[0].Metadata["database"])
	assert.Equal(t, "internal", clickHouseUsernameForRoute(chConfigs, authorizer, coreCtx))

	nonCoreCtx := withAuthUser(context.Background(), &AuthUser{Groups: []string{"other-org"}})
	filtered = authorizer.FilterDatasources(nonCoreCtx, resp)
	assert.Equal(t, []string{"clickhouse-raw"}, filtered.ClickHouse)
	require.Len(t, filtered.ClickHouseInfo, 1)
	assert.Equal(t, "external", filtered.ClickHouseInfo[0].Metadata["database"])
	assert.Equal(t, "external", clickHouseUsernameForRoute(chConfigs, authorizer, nonCoreCtx))
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
						AllowedOrgs: []string{"ethpandaops:Core"},
						Host:        "clickhouse.internal.example.com",
						Username:    "pandaproxy_internal",
						Password:    "secret",
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

	_, ok := authorizer.routeName(ctx, "clickhouse", "clickhouse-raw")
	assert.False(t, ok)

	filtered := authorizer.FilterDatasources(ctx, DatasourcesResponse{
		ClickHouse:     []string{"clickhouse-raw"},
		ClickHouseInfo: []types.DatasourceInfo{{Type: "clickhouse", Name: "clickhouse-raw"}},
	})
	assert.Empty(t, filtered.ClickHouse)
	assert.Empty(t, filtered.ClickHouseInfo)
}

func TestAuthenticatedUserWithNoOrgsCannotUseRestrictedVariant(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		ClickHouse: []ClickHouseClusterConfig{
			{
				BaseDatasourceConfig: BaseDatasourceConfig{Name: "clickhouse-raw"},
				Variants: []ClickHouseClusterVariantConfig{
					{AllowedOrgs: []string{"ethpandaops:Core"}, Host: "clickhouse.internal.example.com", Username: "internal", Password: "secret"},
				},
			},
		},
	}
	cfg.ApplyDefaults()
	require.NoError(t, cfg.Validate())

	authorizer := NewAuthorizer(logrus.New(), cfg)
	resp := DatasourcesResponse{
		ClickHouse:     []string{"clickhouse-raw"},
		ClickHouseInfo: []types.DatasourceInfo{{Type: "clickhouse", Name: "clickhouse-raw"}},
	}

	for _, tt := range []struct {
		name   string
		groups []string
	}{
		{name: "nil groups", groups: nil},
		{name: "empty groups", groups: []string{}},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := withAuthUser(context.Background(), &AuthUser{Groups: tt.groups})
			assert.False(t, authorizer.isAllowed(ctx, "clickhouse", "clickhouse-raw"))
			assert.Empty(t, authorizer.FilterDatasources(ctx, resp).ClickHouse)
		})
	}
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
			wantErr: `clickhouse[0] "mixed" cannot mix top-level backend fields with variants`,
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
			wantErr: `prometheus[0] "mixed" cannot mix top-level backend fields with variants`,
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
			wantErr: `loki[0] "mixed" cannot mix top-level backend fields with variants`,
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
			wantErr: `prometheus[0] "mixed" cannot set top-level allowed_orgs with variants`,
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

func TestDatasourceVariantMiddlewareRoutesToSelectedBackend(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name       string
		path       string
		datasource string
		config     func(internal, external *httptest.Server) ServerConfig
	}{
		{
			name:       "clickhouse",
			path:       "/clickhouse",
			datasource: "clickhouse-raw",
			config: func(internal, external *httptest.Server) ServerConfig {
				internalHost, internalPort := clickHouseServerAddr(t, internal)
				externalHost, externalPort := clickHouseServerAddr(t, external)

				return ServerConfig{
					Auth: AuthConfig{Mode: AuthModeNone},
					ClickHouse: []ClickHouseClusterConfig{
						{
							BaseDatasourceConfig: BaseDatasourceConfig{Name: "clickhouse-raw"},
							Variants: []ClickHouseClusterVariantConfig{
								{AllowedOrgs: []string{"ethpandaops:Core"}, Host: internalHost, Port: internalPort, Username: "internal", Password: "secret"},
								{Host: externalHost, Port: externalPort, Username: "external", Password: "secret"},
							},
						},
					},
				}
			},
		},
		{
			name:       "prometheus",
			path:       "/prometheus",
			datasource: "prometheus-main",
			config: func(internal, external *httptest.Server) ServerConfig {
				return ServerConfig{
					Auth: AuthConfig{Mode: AuthModeNone},
					Prometheus: []PrometheusInstanceConfig{
						{
							BaseDatasourceConfig: BaseDatasourceConfig{Name: "prometheus-main"},
							Variants: []PrometheusInstanceVariantConfig{
								{AllowedOrgs: []string{"ethpandaops:Core"}, URL: internal.URL, Username: "internal"},
								{URL: external.URL, Username: "external"},
							},
						},
					},
				}
			},
		},
		{
			name:       "loki",
			path:       "/loki",
			datasource: "loki-main",
			config: func(internal, external *httptest.Server) ServerConfig {
				return ServerConfig{
					Auth: AuthConfig{Mode: AuthModeNone},
					Loki: []LokiInstanceConfig{
						{
							BaseDatasourceConfig: BaseDatasourceConfig{Name: "loki-main"},
							Variants: []LokiInstanceVariantConfig{
								{AllowedOrgs: []string{"ethpandaops:Core"}, URL: internal.URL, Username: "internal"},
								{URL: external.URL, Username: "external"},
							},
						},
					},
				}
			},
		},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			internal := upstreamAuthServer("internal")
			defer internal.Close()

			external := upstreamAuthServer("external")
			defer external.Close()

			cfg := tt.config(internal, external)
			cfg.ApplyDefaults()

			srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
			require.NoError(t, err)

			for _, reqCase := range []struct {
				name   string
				groups []string
				want   string
			}{
				{name: "core", groups: []string{"ethpandaops:Core"}, want: "internal:internal"},
				{name: "catch-all", groups: []string{"other-org"}, want: "external:external"},
			} {
				reqCase := reqCase
				t.Run(reqCase.name, func(t *testing.T) {
					rec := httptest.NewRecorder()
					req := requestWithProxyUser(http.MethodGet, tt.path, reqCase.groups)
					req.Header.Set("X-Datasource", tt.datasource)
					srv.mux.ServeHTTP(rec, req)

					require.Equal(t, http.StatusOK, rec.Code)
					assert.Equal(t, reqCase.want, rec.Body.String())
				})
			}
		})
	}
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

func upstreamAuthServer(label string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, _, _ := r.BasicAuth()
		_, _ = w.Write([]byte(label + ":" + username))
	}))
}

func clickHouseServerAddr(t *testing.T, srv *httptest.Server) (string, int) {
	t.Helper()

	host, portValue, err := net.SplitHostPort(srv.Listener.Addr().String())
	require.NoError(t, err)

	port, err := strconv.Atoi(portValue)
	require.NoError(t, err)

	return host, port
}

func clickHouseUsernameForRoute(configs []handlers.ClickHouseConfig, authorizer *Authorizer, ctx context.Context) string {
	routeName, ok := authorizer.routeName(ctx, "clickhouse", "clickhouse-raw")
	if !ok {
		return ""
	}

	for _, cfg := range configs {
		name := cfg.Name
		if cfg.RouteName != "" {
			name = cfg.RouteName
		}
		if routeName == name {
			return cfg.Username
		}
	}

	return ""
}
