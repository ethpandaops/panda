package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/proxy/handlers"
	"github.com/ethpandaops/panda/pkg/types"
)

func devnetTestConfig() ServerConfig {
	cfg := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		DevnetServices: &DevnetServicesConfig{
			CFAccessClientID:     "id",
			CFAccessClientSecret: "secret",
			Services: []BaseDatasourceConfig{
				{Name: "spamoor", AllowedOrgs: []string{"ethpandaops:Core"}},
				{Name: "assertoor"},
			},
		},
	}
	cfg.ApplyDefaults()

	return cfg
}

func TestDevnetServicesConfigValidate(t *testing.T) {
	services := []BaseDatasourceConfig{{Name: "spamoor"}}

	tests := []struct {
		name    string
		cfg     DevnetServicesConfig
		wantErr string
	}{
		{"cf credential", DevnetServicesConfig{CFAccessClientID: "id", CFAccessClientSecret: "s", Services: services}, ""},
		{"static token", DevnetServicesConfig{StaticToken: "t", Services: services}, ""},
		{"no credential", DevnetServicesConfig{Services: services}, "cf_access_client_id and cf_access_client_secret are required"},
		{"half cf credential", DevnetServicesConfig{CFAccessClientID: "id", Services: services}, "are required"},
		{"both credentials", DevnetServicesConfig{StaticToken: "t", CFAccessClientID: "id", Services: services}, "not both"},
		{"no services", DevnetServicesConfig{StaticToken: "t"}, "must list at least one"},
		{"unknown service", DevnetServicesConfig{StaticToken: "t", Services: []BaseDatasourceConfig{{Name: "dora"}}}, `"dora" is not one of`},
		{"duplicate service", DevnetServicesConfig{StaticToken: "t", Services: []BaseDatasourceConfig{{Name: "spamoor"}, {Name: "spamoor"}}}, "duplicated"},
		{"bad suffix", DevnetServicesConfig{StaticToken: "t", DomainSuffix: "evil.com/x", Services: services}, "not a DNS zone"},
		{"single-label suffix", DevnetServicesConfig{StaticToken: "t", DomainSuffix: "io", Services: services}, "not a DNS zone"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.validate()
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestDevnetServicesAloneIsAValidProxyConfig(t *testing.T) {
	cfg := devnetTestConfig()
	require.NoError(t, cfg.Validate())

	hc := cfg.ToDevnetHandlerConfig()
	require.NotNil(t, hc)
	assert.Equal(t, []string{"spamoor", "assertoor"}, hc.Services)
	assert.Equal(t, "ethpandaops.io", hc.ResolvedSuffix())
}

func TestAuthorizerGatesDevnetServiceFromPath(t *testing.T) {
	a := NewAuthorizer(logrus.New(), devnetTestConfig())
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	mw := a.Middleware()(next)

	tests := []struct {
		name   string
		path   string
		header string
		groups []string
		want   int
	}{
		{"core member reaches spamoor", "/devnet/spamoor/dev-1/api/spammers", "spamoor", []string{"ethpandaops:Core"}, http.StatusTeapot},
		{"outsider denied spamoor", "/devnet/spamoor/dev-1/api/spammers", "spamoor", []string{"sigp"}, http.StatusForbidden},
		{"header cannot unlock another service", "/devnet/spamoor/dev-1/api/spammers", "assertoor", []string{"sigp"}, http.StatusForbidden},
		{"unrestricted assertoor open to all", "/devnet/assertoor/dev-1/api/v1/tests", "", []string{"sigp"}, http.StatusTeapot},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := requestWithProxyUser(http.MethodPost, tt.path, tt.groups)
			if tt.header != "" {
				req.Header.Set(handlers.DatasourceHeader, tt.header)
			}

			rec := httptest.NewRecorder()
			mw.ServeHTTP(rec, req)
			assert.Equal(t, tt.want, rec.Code)
		})
	}
}

func TestDevnetDatasourcesAdvertAndFilter(t *testing.T) {
	srv, err := newServer(logrus.New(), devnetTestConfig(), "http://proxy.test", "18081")
	require.NoError(t, err)

	infos := srv.DevnetDatasourceInfo()
	require.Len(t, infos, 2)
	assert.Equal(t, types.DatasourceInfo{
		Type: "devnet", Name: "spamoor", Metadata: map[string]string{"domain_suffix": "ethpandaops.io"},
	}, infos[0])

	names := func(groups []string) []string {
		req := requestWithProxyUser(http.MethodGet, "/datasources", groups)
		filtered := srv.authorizer.FilterDatasources(req.Context(), DatasourcesResponse{DevnetInfo: infos})

		out := make([]string, 0, len(filtered.DevnetInfo))
		for _, info := range filtered.DevnetInfo {
			out = append(out, info.Name)
		}

		return out
	}

	data, err := json.Marshal(DatasourcesResponse{DevnetInfo: infos})
	require.NoError(t, err)

	var decoded DatasourcesResponse
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, infos, decoded.DevnetInfo, "devnet services survive the wire")

	assert.Equal(t, []string{"spamoor", "assertoor"}, names([]string{"ethpandaops:Core"}))
	assert.Equal(t, []string{"assertoor"}, names([]string{"sigp"}), "restricted services are not advertised")
}
