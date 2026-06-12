package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/proxy"
	"github.com/ethpandaops/panda/pkg/proxy/handlers"
	"github.com/ethpandaops/panda/pkg/types"
)

func newBenchmarkoorService(t *testing.T, transport *recordingTransport, infos ...types.DatasourceInfo) *service {
	t.Helper()

	return testRoutingService(t, transport, []proxy.ClientRoute{{
		Name: "hosted",
		Client: &routingProxyClient{
			url:          "https://hosted.proxy",
			token:        "hosted-token",
			benchmarkoor: infos,
		},
	}})
}

func callBenchmarkoorOp(t *testing.T, svc *service, operationID string, args map[string]any) *httptest.ResponseRecorder {
	t.Helper()

	body, err := json.Marshal(map[string]any{"args": args})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/operations/"+operationID, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	handled := svc.handleBenchmarkoorOperation(operationID, rec, req)
	require.True(t, handled, "operation %s not handled", operationID)

	return rec
}

func TestBenchmarkoorListRunsBuildsQuery(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{body: `{"data":[],"limit":100,"offset":0}`, contentType: "application/json"}
	svc := newBenchmarkoorService(t, transport, types.DatasourceInfo{Name: "production"})

	rec := callBenchmarkoorOp(t, svc, "benchmarkoor.list_runs", map[string]any{
		"client":  "geth",
		"status":  "completed",
		"order":   "timestamp.desc",
		"limit":   25,
		"offset":  5,
		"filters": map[string]any{"tests_failed": "gt.0"},
	})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, transport.last)

	assert.Equal(t, "/benchmarkoor/api/v1/index/query/runs", transport.last.URL.Path)
	assert.Equal(t, "production", transport.last.Header.Get(handlers.DatasourceHeader))
	assert.Equal(t, "Bearer hosted-token", transport.last.Header.Get("Authorization"))

	query := transport.last.URL.Query()
	assert.Equal(t, "eq.geth", query.Get("client"))
	assert.Equal(t, "eq.completed", query.Get("status"))
	assert.Equal(t, "timestamp.desc", query.Get("order"))
	assert.Equal(t, "25", query.Get("limit"))
	assert.Equal(t, "5", query.Get("offset"))
	assert.Equal(t, "gt.0", query.Get("tests_failed"))
}

func TestBenchmarkoorDatasourceDefaultsWhenSingle(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{body: `{"data":[]}`, contentType: "application/json"}
	svc := newBenchmarkoorService(t, transport, types.DatasourceInfo{Name: "production"})

	rec := callBenchmarkoorOp(t, svc, "benchmarkoor.list_suites", map[string]any{})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "production", transport.last.Header.Get(handlers.DatasourceHeader))
}

func TestBenchmarkoorDatasourceRequiredWhenAmbiguous(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{body: `{}`}
	svc := newBenchmarkoorService(t, transport,
		types.DatasourceInfo{Name: "production"},
		types.DatasourceInfo{Name: "staging"},
	)

	rec := callBenchmarkoorOp(t, svc, "benchmarkoor.list_runs", map[string]any{})

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "production")
	assert.Contains(t, rec.Body.String(), "staging")
}

func TestBenchmarkoorUnknownDatasource(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{body: `{}`}
	svc := newBenchmarkoorService(t, transport, types.DatasourceInfo{Name: "production"})

	rec := callBenchmarkoorOp(t, svc, "benchmarkoor.list_runs", map[string]any{"datasource": "nope"})

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestBenchmarkoorFilterValidation(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{body: `{}`}
	svc := newBenchmarkoorService(t, transport, types.DatasourceInfo{Name: "production"})

	for name, filters := range map[string]map[string]any{
		"reserved param":     {"limit": "eq.1"},
		"invalid key":        {"client;drop": "eq.geth"},
		"non-string value":   {"client": 7},
		"uppercase key":      {"Client": "eq.geth"},
		"empty filter value": {"client": ""},
	} {
		rec := callBenchmarkoorOp(t, svc, "benchmarkoor.list_runs", map[string]any{"filters": filters})
		assert.Equal(t, http.StatusBadRequest, rec.Code, "filters %s should be rejected", name)
	}

	assert.Nil(t, transport.last, "invalid filters must not reach the proxy")
}

func TestBenchmarkoorGetRunUnwrapsEnvelope(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{
		body:        `{"data":[{"run_id":"abc123","client":"geth"}],"limit":1,"offset":0}`,
		contentType: "application/json",
	}
	svc := newBenchmarkoorService(t, transport, types.DatasourceInfo{Name: "production"})

	rec := callBenchmarkoorOp(t, svc, "benchmarkoor.get_run", map[string]any{"run_id": "abc123"})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"run_id":"abc123","client":"geth"}`, rec.Body.String())

	query := transport.last.URL.Query()
	assert.Equal(t, "eq.abc123", query.Get("run_id"))
	assert.Equal(t, "1", query.Get("limit"))
}

func TestBenchmarkoorGetRunNotFound(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{body: `{"data":[],"limit":1,"offset":0}`, contentType: "application/json"}
	svc := newBenchmarkoorService(t, transport, types.DatasourceInfo{Name: "production"})

	rec := callBenchmarkoorOp(t, svc, "benchmarkoor.get_run", map[string]any{"run_id": "missing"})

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestBenchmarkoorSuiteStats(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{body: `{}`, contentType: "application/json"}
	svc := newBenchmarkoorService(t, transport, types.DatasourceInfo{Name: "production"})

	rec := callBenchmarkoorOp(t, svc, "benchmarkoor.get_suite_stats", map[string]any{
		"suite_hash":          "deadbeef",
		"max_runs_per_client": 5,
	})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "/benchmarkoor/api/v1/index/suites/deadbeef/stats", transport.last.URL.Path)
	assert.Equal(t, "5", transport.last.URL.Query().Get("max_runs_per_client"))
}

func TestBenchmarkoorGetFileRejectsTraversal(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{body: `{}`}
	svc := newBenchmarkoorService(t, transport, types.DatasourceInfo{Name: "production"})

	for _, path := range []string{"", "../etc/passwd", "results/../../secret"} {
		rec := callBenchmarkoorOp(t, svc, "benchmarkoor.get_file", map[string]any{"path": path})
		assert.Equal(t, http.StatusBadRequest, rec.Code, "path %q should be rejected", path)
	}

	assert.Nil(t, transport.last)
}

func TestBenchmarkoorGetFilePassthrough(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{body: `{"run_id":"abc123"}`, contentType: "application/json"}
	svc := newBenchmarkoorService(t, transport, types.DatasourceInfo{Name: "production"})

	rec := callBenchmarkoorOp(t, svc, "benchmarkoor.get_file", map[string]any{
		"path": "results/runs/abc123/result.json",
	})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "/benchmarkoor/api/v1/files/results/runs/abc123/result.json", transport.last.URL.Path)
}

func TestBenchmarkoorLinks(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{body: `{}`}
	svc := newBenchmarkoorService(t, transport, types.DatasourceInfo{
		Name: "production",
		Metadata: map[string]string{
			"url":    "https://benchmarkoor-api.example.com",
			"ui_url": "https://benchmarkoor.example.com/",
		},
	})

	rec := callBenchmarkoorOp(t, svc, "benchmarkoor.link_run", map[string]any{"run_id": "abc123"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"https://benchmarkoor.example.com/runs/abc123"`)

	rec = callBenchmarkoorOp(t, svc, "benchmarkoor.link_suite", map[string]any{"suite_hash": "deadbeef"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"https://benchmarkoor.example.com/suites/deadbeef"`)
}

func TestBenchmarkoorLinkWithoutUIURL(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{body: `{}`}
	svc := newBenchmarkoorService(t, transport, types.DatasourceInfo{Name: "production"})

	rec := callBenchmarkoorOp(t, svc, "benchmarkoor.link_run", map[string]any{"run_id": "abc123"})

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "ui_url")
}

func TestBenchmarkoorListDatasources(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{body: `{}`}
	svc := newBenchmarkoorService(t, transport,
		types.DatasourceInfo{Name: "staging", Description: "staging results"},
		types.DatasourceInfo{Name: "production", Metadata: map[string]string{"url": "https://api.example.com"}},
	)

	rec := callBenchmarkoorOp(t, svc, "benchmarkoor.list_datasources", nil)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var response struct {
		Data struct {
			Datasources []listItem `json:"datasources"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	require.Len(t, response.Data.Datasources, 2)
	assert.Equal(t, "production", response.Data.Datasources[0].Name)
	assert.Equal(t, "https://api.example.com", response.Data.Datasources[0].URL)
	assert.Equal(t, "staging", response.Data.Datasources[1].Name)
}
