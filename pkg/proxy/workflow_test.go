package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/types"
)

func TestWorkflowAdvertWireRoundTrip(t *testing.T) {
	t.Parallel()

	orig := DatasourcesResponse{
		ClickHouseInfo: []types.DatasourceInfo{{Type: "clickhouse", Name: "ch"}},
		Workflow:       &WorkflowAdvert{Enabled: true, WebURL: "https://ui.example.io"},
	}

	data, err := json.Marshal(orig)
	require.NoError(t, err)

	// The token is never in the wire (there is none here, but the advert shape
	// itself must never carry a credential field).
	assert.NotContains(t, string(data), "api_token")
	assert.Contains(t, string(data), `"workflow"`)
	assert.Contains(t, string(data), `"web_url":"https://ui.example.io"`)

	var got DatasourcesResponse
	require.NoError(t, json.Unmarshal(data, &got))

	require.NotNil(t, got.Workflow)
	assert.True(t, got.Workflow.Enabled)
	assert.Equal(t, "https://ui.example.io", got.Workflow.WebURL)
	// The legacy name-only clickhouse list must still round-trip alongside it.
	require.Len(t, got.ClickHouseInfo, 1)
	assert.Equal(t, "ch", got.ClickHouseInfo[0].Name)
}

func TestWorkflowAdvertAbsentWhenNil(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal(DatasourcesResponse{})
	require.NoError(t, err)
	assert.NotContains(t, string(data), "workflow")

	// A legacy name-only list (older peer) unmarshals with no workflow advert.
	var got DatasourcesResponse
	require.NoError(t, json.Unmarshal([]byte(`{"clickhouse":["ch"]}`), &got))
	assert.Nil(t, got.Workflow)
	require.Len(t, got.ClickHouseInfo, 1)
	assert.Equal(t, "ch", got.ClickHouseInfo[0].Name)
}

func TestWorkflowAdvertOrgFilteringHidesAdvert(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		Auth:       AuthConfig{Mode: AuthModeOIDC},
		ClickHouse: clickHouseOnly(),
		Workflow:   &WorkflowConfig{URL: "https://workflow.example.io", AllowedOrgs: []string{"ethpandaops"}},
	}
	cfg.ApplyDefaults()

	authz := NewAuthorizer(logrus.New(), cfg)

	resp := DatasourcesResponse{Workflow: &WorkflowAdvert{Enabled: true, WebURL: "https://ui.example.io"}}

	// Member of the allowed org keeps the advert.
	memberCtx := withAuthUser(context.Background(), &AuthUser{Groups: []string{"ethpandaops"}})
	member := authz.FilterDatasources(memberCtx, resp)
	require.NotNil(t, member.Workflow)
	assert.Equal(t, "https://ui.example.io", member.Workflow.WebURL)

	// Non-member loses the advert entirely.
	otherCtx := withAuthUser(context.Background(), &AuthUser{Groups: []string{"sigp"}})
	other := authz.FilterDatasources(otherCtx, resp)
	assert.Nil(t, other.Workflow)
}

func TestWorkflowInfoServerFromConfig(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		ClickHouse: clickHouseOnly(),
		Workflow:   &WorkflowConfig{URL: "https://api.example.io/", APIToken: "tok"},
	}

	s, err := newServer(logrus.New(), cfg, "http://localhost:0", "0")
	require.NoError(t, err)

	enabled, webURL := s.WorkflowInfo()
	assert.True(t, enabled)
	assert.Equal(t, "https://api.example.io", webURL)

	// No workflow configured → disabled.
	cfg2 := ServerConfig{ClickHouse: clickHouseOnly()}
	s2, err := newServer(logrus.New(), cfg2, "http://localhost:0", "0")
	require.NoError(t, err)
	enabled2, webURL2 := s2.WorkflowInfo()
	assert.False(t, enabled2)
	assert.Empty(t, webURL2)
}

func TestWorkflowInfoProxyClientFromCache(t *testing.T) {
	t.Parallel()

	c := &proxyClient{
		datasources: &DatasourcesResponse{
			Workflow: &WorkflowAdvert{Enabled: true, WebURL: "https://ui.example.io"},
		},
	}

	enabled, webURL := c.WorkflowInfo()
	assert.True(t, enabled)
	assert.Equal(t, "https://ui.example.io", webURL)

	empty := &proxyClient{datasources: &DatasourcesResponse{}}
	enabled2, webURL2 := empty.WorkflowInfo()
	assert.False(t, enabled2)
	assert.Empty(t, webURL2)
}

func TestWorkflowInfoRouterPrimaryLacksSecondaryHas(t *testing.T) {
	t.Parallel()

	// Primary does not advertise; a secondary route does. Selection must pick
	// the secondary, NOT Primary().
	primary := &fakeRouterClient{url: "https://primary.example"}
	secondary := &fakeRouterClient{url: "https://secondary.example", workflowEnabled: true, workflowWebURL: "https://ui.example.io"}

	router := NewRouter(logrus.New(), []ClientRoute{
		{Name: "primary", Client: primary},
		{Name: "secondary", Client: secondary},
	})

	enabled, webURL := router.(WorkflowInfoProvider).WorkflowInfo()
	assert.True(t, enabled)
	assert.Equal(t, "https://ui.example.io", webURL)

	route, ok := router.WorkflowRoute()
	require.True(t, ok)
	assert.Equal(t, "https://secondary.example", route.URL())

	// The primary is still the first external proxy, distinct from the workflow route.
	assert.Equal(t, "https://primary.example", router.Primary().URL())
}

func TestWorkflowInfoRouterFirstAdvertisingWins(t *testing.T) {
	t.Parallel()

	first := &fakeRouterClient{url: "https://a.example", workflowEnabled: true, workflowWebURL: "https://a-ui.example"}
	second := &fakeRouterClient{url: "https://b.example", workflowEnabled: true, workflowWebURL: "https://b-ui.example"}

	router := NewRouter(logrus.New(), []ClientRoute{
		{Name: "a", Client: first},
		{Name: "b", Client: second},
	})

	enabled, webURL := router.(WorkflowInfoProvider).WorkflowInfo()
	assert.True(t, enabled)
	assert.Equal(t, "https://a-ui.example", webURL)

	route, ok := router.WorkflowRoute()
	require.True(t, ok)
	assert.Equal(t, "https://a.example", route.URL())
}

func TestWorkflowInfoRouterNoneAdvertise(t *testing.T) {
	t.Parallel()

	router := NewRouter(logrus.New(), []ClientRoute{
		{Name: "a", Client: &fakeRouterClient{url: "https://a.example"}},
	})

	enabled, _ := router.(WorkflowInfoProvider).WorkflowInfo()
	assert.False(t, enabled)

	_, ok := router.WorkflowRoute()
	assert.False(t, ok)
}

// newWorkflowMetricsServer builds an auth-none proxy server whose /workflow
// route targets the given upstream in token mode.
func newWorkflowMetricsServer(t *testing.T, upstreamURL string) *server {
	t.Helper()

	cfg := ServerConfig{
		Auth:       AuthConfig{Mode: AuthModeNone},
		ClickHouse: clickHouseOnly(),
		Workflow:   &WorkflowConfig{URL: upstreamURL, APIToken: "tok"},
	}

	s, err := newServer(logrus.New(), cfg, "http://localhost:0", "0")
	require.NoError(t, err)

	return s
}

func durationSampleCount(t *testing.T, method string) uint64 {
	t.Helper()

	obs, err := ProxyRequestDurationSeconds.GetMetricWithLabelValues("workflow", "default", method)
	require.NoError(t, err)

	m := &dto.Metric{}
	require.NoError(t, obs.(interface{ Write(*dto.Metric) error }).Write(m))

	return m.GetHistogram().GetSampleCount()
}

func counterValue(t *testing.T, method, status string) float64 {
	t.Helper()

	c, err := ProxyRequestsTotal.GetMetricWithLabelValues("workflow", "default", method, status)
	require.NoError(t, err)

	m := &dto.Metric{}
	require.NoError(t, c.Write(m))

	return m.GetCounter().GetValue()
}

func TestWorkflowRouteMetricsUseWorkflowType(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	s := newWorkflowMetricsServer(t, upstream.URL)

	// PUT is a real method no other workflow test uses, isolating the series.
	const method = http.MethodPut

	beforeCounter := counterValue(t, method, "200")
	beforeDuration := durationSampleCount(t, method)

	req := httptest.NewRequest(method, "/workflow/whiteboards", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	assert.InDelta(t, beforeCounter+1, counterValue(t, method, "200"), 0.0001,
		"workflow request counter should increment")
	assert.Equal(t, beforeDuration+1, durationSampleCount(t, method),
		"non-SSE workflow request duration should be observed")
}

func TestWorkflowSSEExcludedFromDuration(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		if f, ok := w.(http.Flusher); ok {
			_, _ = io.WriteString(w, "data: hi\n\n")
			f.Flush()
		}
	}))
	defer upstream.Close()

	s := newWorkflowMetricsServer(t, upstream.URL)

	// PATCH isolates the (method, status) series from the non-SSE test above.
	const method = http.MethodPatch

	beforeCounter := counterValue(t, method, "200")
	beforeDuration := durationSampleCount(t, method)

	req := httptest.NewRequest(method, "/workflow/workflows/wf_1/runs/run_1/state/stream", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	assert.InDelta(t, beforeCounter+1, counterValue(t, method, "200"), 0.0001,
		"workflow SSE request counter should still increment")
	assert.Equal(t, beforeDuration, durationSampleCount(t, method),
		"workflow SSE duration must be excluded from the histogram")
}
