package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

	resp := DatasourcesResponse{
		ClickHouse:     []string{"restricted", "public"},
		ClickHouseInfo: []types.DatasourceInfo{{Type: "clickhouse", Name: "restricted"}, {Type: "clickhouse", Name: "public"}},
		Prometheus:     []string{"internal"},
		PrometheusInfo: []types.DatasourceInfo{{Type: "prometheus", Name: "internal"}},
		Loki:           []string{"logs"},
		LokiInfo:       []types.DatasourceInfo{{Type: "loki", Name: "logs"}},
	}

	// User in ethpandaops — should see everything.
	ctx := withAuthUser(context.Background(), &AuthUser{Groups: []string{"ethpandaops"}})
	filtered := authorizer.FilterDatasources(ctx, resp)
	assert.Equal(t, []string{"restricted", "public"}, filtered.ClickHouse)
	assert.Equal(t, []string{"internal"}, filtered.Prometheus)
	assert.Equal(t, []string{"logs"}, filtered.Loki)

	// User in sigp — should see public clickhouse + internal prometheus + logs.
	ctx = withAuthUser(context.Background(), &AuthUser{Groups: []string{"sigp"}})
	filtered = authorizer.FilterDatasources(ctx, resp)
	assert.Equal(t, []string{"public"}, filtered.ClickHouse)
	assert.Equal(t, []string{"internal"}, filtered.Prometheus)
	assert.Equal(t, []string{"logs"}, filtered.Loki)

	// User in unknown org — only unrestricted datasources.
	ctx = withAuthUser(context.Background(), &AuthUser{Groups: []string{"unknown"}})
	filtered = authorizer.FilterDatasources(ctx, resp)
	assert.Equal(t, []string{"public"}, filtered.ClickHouse)
	assert.Empty(t, filtered.Prometheus)
	assert.Equal(t, []string{"logs"}, filtered.Loki)

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
