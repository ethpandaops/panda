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
	logtest "github.com/sirupsen/logrus/hooks/test"
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
			{BaseDatasourceConfig: BaseDatasourceConfig{Name: "public", AllowedOrgs: []string{AllowAllAuthenticated}}, Host: "example.com", Port: 8123, Username: "u", Password: "p"},
			{BaseDatasourceConfig: BaseDatasourceConfig{Name: "unset"}, Host: "example.com", Port: 8123, Username: "u", Password: "p"},
		},
		Prometheus: []PrometheusInstanceConfig{
			{BaseDatasourceConfig: BaseDatasourceConfig{Name: "internal", AllowedOrgs: []string{"ethpandaops", "sigp"}}, URL: "https://prom.example.com"},
		},
		Loki: []LokiInstanceConfig{
			{BaseDatasourceConfig: BaseDatasourceConfig{Name: "logs", AllowedOrgs: []string{AllowAllAuthenticated}}, URL: "https://loki.example.com"},
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

func TestAuthorizerMiddlewareWildcardAllowsAnyAuthenticatedUser(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	require.NoError(t, err)

	// "*" admits identities that carry no org group at all (CI accounts,
	// Dex tokens with only org:team groups).
	for _, groups := range [][]string{{"panda-ci"}, {"ethpandaops:Core"}, nil} {
		rec := httptest.NewRecorder()
		req := requestWithProxyUser(http.MethodPost, "/clickhouse", groups)
		req.Header.Set("X-Datasource", "public")
		srv.mux.ServeHTTP(rec, req)

		assert.NotEqual(t, http.StatusForbidden, rec.Code, "groups %v should reach a \"*\" datasource", groups)
	}
}

func TestAuthorizerMiddlewareDefaultDeny(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	require.NoError(t, err)

	for _, tt := range []struct {
		name       string
		datasource string
	}{
		{name: "datasource without allowed_orgs", datasource: "unset"},
		{name: "unknown datasource name", datasource: "does-not-exist"},
		{name: "missing datasource header", datasource: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := requestWithProxyUser(http.MethodPost, "/clickhouse", []string{"ethpandaops"})
			if tt.datasource != "" {
				req.Header.Set("X-Datasource", tt.datasource)
			}
			srv.mux.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusForbidden, rec.Code)
		})
	}
}

func TestAuthorizerMiddlewareAllowsNoAuthUser(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	require.NoError(t, err)

	// No auth user (none mode) — allowed_orgs is not enforced, so even
	// datasources without allowed_orgs and unknown names pass through to the
	// handler (which 404s the unknown one).
	for _, datasource := range []string{"restricted", "unset", "does-not-exist"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/clickhouse", nil)
		req.Header.Set("X-Datasource", datasource)
		srv.mux.ServeHTTP(rec, req)

		assert.NotEqual(t, http.StatusForbidden, rec.Code, "no auth user should pass through for %q", datasource)
	}
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

	resp := DatasourcesResponse{
		ClickHouseInfo:     []types.DatasourceInfo{{Type: "clickhouse", Name: "restricted"}, {Type: "clickhouse", Name: "public"}, {Type: "clickhouse", Name: "unset"}, {Type: "clickhouse", Name: "unconfigured"}},
		PrometheusInfo:     []types.DatasourceInfo{{Type: "prometheus", Name: "internal"}},
		LokiInfo:           []types.DatasourceInfo{{Type: "loki", Name: "logs"}},
		EmbeddingAvailable: true,
		EmbeddingModel:     "openai/text-embedding-3-small",
	}

	// Embedding is infrastructure metadata, not a per-user datasource — it must
	// survive filtering unconditionally regardless of org membership.
	assertEmbeddingPreserved := func(t *testing.T, filtered DatasourcesResponse) {
		t.Helper()
		assert.True(t, filtered.EmbeddingAvailable)
		assert.Equal(t, "openai/text-embedding-3-small", filtered.EmbeddingModel)
	}

	// User in ethpandaops — sees everything that has allowed_orgs set and
	// matching; never "unset" (empty allowed_orgs) or "unconfigured" (no rule).
	ctx := withAuthUser(context.Background(), &AuthUser{Groups: []string{"ethpandaops"}})
	filtered := authorizer.FilterDatasources(ctx, resp)
	assert.Equal(t, []string{"restricted", "public"}, datasourceNames(filtered.ClickHouseInfo))
	assert.Equal(t, []string{"internal"}, datasourceNames(filtered.PrometheusInfo))
	assert.Equal(t, []string{"logs"}, datasourceNames(filtered.LokiInfo))
	assertEmbeddingPreserved(t, filtered)

	// User in sigp — should see public clickhouse + internal prometheus + logs ("*").
	ctx = withAuthUser(context.Background(), &AuthUser{Groups: []string{"sigp"}})
	filtered = authorizer.FilterDatasources(ctx, resp)
	assert.Equal(t, []string{"public"}, datasourceNames(filtered.ClickHouseInfo))
	assert.Equal(t, []string{"internal"}, datasourceNames(filtered.PrometheusInfo))
	assert.Equal(t, []string{"logs"}, datasourceNames(filtered.LokiInfo))
	assertEmbeddingPreserved(t, filtered)

	// User in unknown org — only "*" datasources.
	ctx = withAuthUser(context.Background(), &AuthUser{Groups: []string{"unknown"}})
	filtered = authorizer.FilterDatasources(ctx, resp)
	assert.Equal(t, []string{"public"}, datasourceNames(filtered.ClickHouseInfo))
	assert.Empty(t, filtered.PrometheusInfo)
	assert.Equal(t, []string{"logs"}, datasourceNames(filtered.LokiInfo))
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
	assert.Contains(t, datasourceNames(resp.ClickHouseInfo), "restricted")
	assert.Contains(t, datasourceNames(resp.ClickHouseInfo), "public")

	assert.NotContains(t, datasourceNames(resp.ClickHouseInfo), "unset")

	// User in unknown org — should only see "*" datasources.
	rec = httptest.NewRecorder()
	req = requestWithProxyUser(http.MethodGet, "/datasources", []string{"unknown"})
	srv.mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	resp = DatasourcesResponse{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, []string{"public"}, datasourceNames(resp.ClickHouseInfo))
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
					{AllowedOrgs: []string{AllowAllAuthenticated}, Host: "external.example.com", Database: "external", Username: "external", Password: "secret"},
				},
			},
		},
	}
	cfg.ApplyDefaults()
	require.NoError(t, cfg.Validate())

	authorizer := NewAuthorizer(logrus.New(), cfg)
	chConfigs, _, _, _ := cfg.ToHandlerConfigs()
	resp := DatasourcesResponse{
		ClickHouseInfo: []types.DatasourceInfo{{Type: "clickhouse", Name: "clickhouse-raw"}},
	}

	coreCtx := withAuthUser(context.Background(), &AuthUser{Groups: []string{"ethpandaops:Core"}})
	filtered := authorizer.FilterDatasources(coreCtx, resp)
	assert.Equal(t, []string{"clickhouse-raw"}, datasourceNames(filtered.ClickHouseInfo))
	require.Len(t, filtered.ClickHouseInfo, 1)
	assert.Equal(t, "internal", filtered.ClickHouseInfo[0].Metadata["database"])
	assert.Equal(t, "internal", clickHouseUsernameForRoute(chConfigs, authorizer, coreCtx))

	nonCoreCtx := withAuthUser(context.Background(), &AuthUser{Groups: []string{"other-org"}})
	filtered = authorizer.FilterDatasources(nonCoreCtx, resp)
	assert.Equal(t, []string{"clickhouse-raw"}, datasourceNames(filtered.ClickHouseInfo))
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
		ClickHouseInfo: []types.DatasourceInfo{{Type: "clickhouse", Name: "clickhouse-raw"}},
	})
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
			assert.Empty(t, authorizer.FilterDatasources(ctx, resp).ClickHouseInfo)
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
								{AllowedOrgs: []string{AllowAllAuthenticated}, Host: externalHost, Port: externalPort, Username: "external", Password: "secret"},
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
								{AllowedOrgs: []string{AllowAllAuthenticated}, URL: external.URL, Username: "external"},
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
								{AllowedOrgs: []string{AllowAllAuthenticated}, URL: external.URL, Username: "external"},
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

	const beaconPath = "/beacon/mainnet/lighthouse/eth/v1/node/version"

	for _, tt := range []struct {
		name        string
		allowedOrgs []string
		groups      []string
		wantAllowed bool
	}{
		{name: "matching org", allowedOrgs: []string{"ethpandaops"}, groups: []string{"ethpandaops"}, wantAllowed: true},
		{name: "wrong org", allowedOrgs: []string{"ethpandaops"}, groups: []string{"other"}},
		{name: "no allowed_orgs denies everyone", groups: []string{"ethpandaops"}},
		{name: "wildcard allows group-less identity", allowedOrgs: []string{AllowAllAuthenticated}, groups: []string{"panda-ci"}, wantAllowed: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := ServerConfig{
				Auth: AuthConfig{Mode: AuthModeNone},
				ClickHouse: []ClickHouseClusterConfig{
					{BaseDatasourceConfig: BaseDatasourceConfig{Name: "ch", AllowedOrgs: []string{AllowAllAuthenticated}}, Host: "example.com", Port: 8123, Username: "u", Password: "p"},
				},
				EthNode: &EthNodeInstanceConfig{
					BaseDatasourceConfig: BaseDatasourceConfig{AllowedOrgs: tt.allowedOrgs},
					Username:             "u",
					Password:             "p",
				},
			}
			cfg.ApplyDefaults()

			srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
			require.NoError(t, err)

			rec := httptest.NewRecorder()
			req := requestWithProxyUser(http.MethodGet, beaconPath, tt.groups)
			srv.mux.ServeHTTP(rec, req)

			if tt.wantAllowed {
				assert.NotEqual(t, http.StatusForbidden, rec.Code)
			} else {
				assert.Equal(t, http.StatusForbidden, rec.Code)
			}

			// The listing must agree with the route.
			rec = httptest.NewRecorder()
			req = requestWithProxyUser(http.MethodGet, "/datasources", tt.groups)
			srv.mux.ServeHTTP(rec, req)
			require.Equal(t, http.StatusOK, rec.Code)

			var resp DatasourcesResponse
			require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
			assert.Equal(t, tt.wantAllowed, resp.EthNodeAvailable)

			// None mode (no user) is unrestricted.
			ctx := context.Background()
			assert.True(t, srv.authorizer.isAllowed(ctx, "ethnode", ""))
			assert.True(t, srv.authorizer.FilterDatasources(ctx, DatasourcesResponse{EthNodeAvailable: true}).EthNodeAvailable)
		})
	}
}

func TestAuthorizerVariantWithoutAllowedOrgsIsUnreachable(t *testing.T) {
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

	// The variant without allowed_orgs is not a catch-all.
	otherCtx := withAuthUser(context.Background(), &AuthUser{Groups: []string{"other-org"}})
	assert.False(t, authorizer.isAllowed(otherCtx, "clickhouse", "clickhouse-raw"))

	coreCtx := withAuthUser(context.Background(), &AuthUser{Groups: []string{"ethpandaops:Core"}})
	assert.Equal(t, "internal", clickHouseUsernameForRoute(chConfigs, authorizer, coreCtx))

	// None mode still selects the first variant.
	assert.Equal(t, "internal", clickHouseUsernameForRoute(chConfigs, authorizer, context.Background()))
}

func TestAuthorizerRouteTypes(t *testing.T) {
	t.Parallel()

	gated := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		EthNode: &EthNodeInstanceConfig{
			BaseDatasourceConfig: BaseDatasourceConfig{AllowedOrgs: []string{"ethpandaops"}},
			Username:             "u",
			Password:             "p",
		},
		Faucet:  &FaucetInstanceConfig{AllowedOrgs: []string{"ethpandaops:Core"}},
		GitHub:  &GitHubAPIConfig{Token: "t", AllowedOrgs: []string{"ethpandaops"}},
		Uploads: &UploadsConfig{Bucket: "b", AllowedOrgs: []string{AllowAllAuthenticated}},
	}

	// Features configured without allowed_orgs. The faucet is enabled through
	// the ethnode credential fallback; ethnode's "*" must not extend to it.
	unset := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		EthNode: &EthNodeInstanceConfig{
			BaseDatasourceConfig: BaseDatasourceConfig{AllowedOrgs: []string{AllowAllAuthenticated}},
			Username:             "u",
			Password:             "p",
		},
		GitHub:  &GitHubAPIConfig{Token: "t"},
		Uploads: &UploadsConfig{Bucket: "b"},
	}

	noUser := []string{"<none>"}

	for _, tt := range []struct {
		name        string
		cfg         ServerConfig
		path        string
		groups      []string // noUser = no authenticated user (auth mode none)
		wantAllowed bool
	}{
		{name: "github matching org", cfg: gated, path: "/github/workflows/dispatch", groups: []string{"ethpandaops"}, wantAllowed: true},
		{name: "github other org", cfg: gated, path: "/github/workflows/dispatch", groups: []string{"panda-ci"}},
		{name: "uploads wildcard", cfg: gated, path: "/uploads", groups: []string{"panda-ci"}, wantAllowed: true},
		{name: "faucet matching org", cfg: gated, path: "/faucet/mainnet/claim", groups: []string{"ethpandaops:Core"}, wantAllowed: true},
		{name: "faucet other org", cfg: gated, path: "/faucet/mainnet/claim", groups: []string{"ethpandaops"}},
		{name: "ethnode execution route", cfg: gated, path: "/execution/mainnet/geth", groups: []string{"panda-ci"}},
		{name: "datasources listing", cfg: gated, path: "/datasources", groups: []string{"panda-ci"}, wantAllowed: true},
		{name: "embed v1", cfg: gated, path: "/embed", groups: []string{"panda-ci"}, wantAllowed: true},
		{name: "embedding v2 check", cfg: gated, path: "/v2/embedding/check", groups: []string{}, wantAllowed: true},
		{name: "embedding v3", cfg: gated, path: "/v3/embedding", groups: []string{"panda-ci"}, wantAllowed: true},
		{name: "unrecognised path", cfg: gated, path: "/something-else", groups: []string{"ethpandaops"}},
		{name: "unrecognised embedding version", cfg: gated, path: "/v4/embedding", groups: []string{"ethpandaops"}},
		{name: "github without allowed_orgs", cfg: unset, path: "/github/workflows/dispatch", groups: []string{"ethpandaops"}},
		{name: "uploads without allowed_orgs", cfg: unset, path: "/uploads", groups: []string{"ethpandaops"}},
		{name: "faucet without allowed_orgs", cfg: unset, path: "/faucet/mainnet/claim", groups: []string{"ethpandaops"}},
		{name: "ethnode wildcard", cfg: unset, path: "/beacon/mainnet/lighthouse", groups: []string{"panda-ci"}, wantAllowed: true},
		{name: "none mode github", cfg: unset, path: "/github/workflows/dispatch", groups: noUser, wantAllowed: true},
		{name: "none mode uploads", cfg: unset, path: "/uploads", groups: noUser, wantAllowed: true},
		{name: "none mode faucet", cfg: unset, path: "/faucet/mainnet/claim", groups: noUser, wantAllowed: true},
		{name: "none mode unrecognised path", cfg: unset, path: "/something-else", groups: noUser, wantAllowed: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			authorizer := NewAuthorizer(logrus.New(), tt.cfg)
			handler := authorizer.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(http.MethodPost, tt.path, nil)
			if len(tt.groups) != 1 || tt.groups[0] != noUser[0] {
				req = requestWithProxyUser(http.MethodPost, tt.path, tt.groups)
			}

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if tt.wantAllowed {
				assert.Equal(t, http.StatusOK, rec.Code)
			} else {
				assert.Equal(t, http.StatusForbidden, rec.Code)
			}
		})
	}
}

func TestNewAuthorizerWarnsAboutUnreachableRules(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		ClickHouse: []ClickHouseClusterConfig{
			{BaseDatasourceConfig: BaseDatasourceConfig{Name: "unset"}},
			{BaseDatasourceConfig: BaseDatasourceConfig{Name: "set", AllowedOrgs: []string{AllowAllAuthenticated}}},
		},
		Prometheus: []PrometheusInstanceConfig{
			{
				BaseDatasourceConfig: BaseDatasourceConfig{Name: "prom"},
				Variants: []PrometheusInstanceVariantConfig{
					{AllowedOrgs: []string{"ethpandaops:Core"}, URL: "https://internal.example.com"},
					{URL: "https://external.example.com"},
				},
			},
		},
		EthNode: &EthNodeInstanceConfig{Username: "u", Password: "p"},
		GitHub:  &GitHubAPIConfig{Token: "t", AllowedOrgs: []string{"ethpandaops"}},
	}

	warnings := func(mode AuthMode) []*logrus.Entry {
		cfg := cfg
		cfg.Auth.Mode = mode

		log, hook := logtest.NewNullLogger()
		NewAuthorizer(log, cfg)

		var entries []*logrus.Entry
		for _, entry := range hook.AllEntries() {
			if entry.Level == logrus.WarnLevel {
				entries = append(entries, entry)
			}
		}

		return entries
	}

	// ethnode, then the faucet that inherits the ethnode credential.
	entries := warnings(AuthModeOIDC)
	require.Len(t, entries, 4)

	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		key, _ := entry.Data["rule"].(string)
		if variant, ok := entry.Data["variant"]; ok {
			key += " variant " + strconv.Itoa(variant.(int))
		}

		got = append(got, key)
		assert.Contains(t, entry.Message, `"*"`)
	}

	assert.Equal(t, []string{"clickhouse:unset", "ethnode", "faucet", "prometheus:prom variant 1"}, got)

	// Without authentication nothing is unreachable, so nothing is logged.
	assert.Empty(t, warnings(AuthModeNone))
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
