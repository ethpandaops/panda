package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethpandaops/panda/pkg/operations"
	"github.com/ethpandaops/panda/pkg/types"
)

func TestPrometheusHandlersValidateArgsAndDefaultQueryFields(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/prometheus/api/v1/query" {
			t.Fatalf("request path = %q, want query path", r.URL.Path)
		}
		if got := r.URL.Query().Get("query"); got != "up" {
			t.Fatalf("query = %q, want up", got)
		}

		_, _ = w.Write([]byte(`{"status":"success","data":{"result":[]}}`))
	}))
	defer upstream.Close()

	srv := newOperationTestService(&stubProxyService{url: upstream.URL}, nil, upstream.Client())

	rec := performOperationRequest(
		t,
		srv,
		"prometheus.query",
		operations.TypedRequest[operations.PrometheusQueryArgs]{Args: operations.PrometheusQueryArgs{
			Datasource: "metrics",
			Query:      "up",
		}},
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("prometheus.query status = %d, want 200", rec.Code)
	}

	rec = performOperationRequest(
		t,
		srv,
		"prometheus.get_label_values",
		operations.TypedRequest[operations.DatasourceLabelArgs]{Args: operations.DatasourceLabelArgs{
			Datasource: "metrics",
		}},
	)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "label is required") {
		t.Fatalf("missing label = (%d, %q), want label is required", rec.Code, rec.Body.String())
	}
}

func TestPrometheusHandlersListDatasourcesAndQueryRangeValidation(t *testing.T) {
	t.Parallel()

	srv := newOperationTestService(&coverageProxyService{
		url: "https://proxy.example",
		prometheusInfo: []types.DatasourceInfo{{
			Name:        "metrics",
			Description: "Cluster metrics",
			Metadata:    map[string]string{"url": "https://prom.example.com"},
		}},
	}, nil, http.DefaultClient)

	rec := httptest.NewRecorder()
	srv.handlePrometheusListDatasources(rec)
	if rec.Code != http.StatusOK {
		t.Fatalf("handlePrometheusListDatasources() status = %d, want 200", rec.Code)
	}

	var response operations.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	payload, err := operations.DecodeResponseData[operations.DatasourcesPayload](&response)
	if err != nil {
		t.Fatalf("DecodeResponseData() error = %v", err)
	}
	if len(payload.Datasources) != 1 || payload.Datasources[0].URL != "https://prom.example.com" {
		t.Fatalf("datasources payload = %#v, want metrics URL", payload.Datasources)
	}

	rec = performOperationRequest(
		t,
		srv,
		"prometheus.query_range",
		operations.TypedRequest[operations.PrometheusRangeQueryArgs]{Args: operations.PrometheusRangeQueryArgs{
			Datasource: "metrics",
			Query:      "up",
		}},
	)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "step is required") {
		t.Fatalf("missing step = (%d, %q), want step is required", rec.Code, rec.Body.String())
	}
}

func TestPrometheusHandlersLabelsAndLabelValues(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/prometheus/api/v1/labels" && r.Header.Get("X-Datasource") != "metrics" {
			t.Fatalf("labels datasource = %q, want metrics", r.Header.Get("X-Datasource"))
		}
		if r.URL.Path == "/prometheus/api/v1/label/job%2Fname/values" && r.Header.Get("X-Datasource") != "metrics" {
			t.Fatalf("label values datasource = %q, want metrics", r.Header.Get("X-Datasource"))
		}
		_, _ = w.Write([]byte(`{"status":"success","data":[]}`))
	}))
	defer upstream.Close()

	srv := newOperationTestService(&stubProxyService{url: upstream.URL}, nil, upstream.Client())

	rec := performOperationRequest(
		t,
		srv,
		"prometheus.get_labels",
		operations.TypedRequest[operations.DatasourceArgs]{Args: operations.DatasourceArgs{
			Datasource: "metrics",
		}},
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("prometheus.get_labels status = %d, want 200", rec.Code)
	}

	rec = performOperationRequest(
		t,
		srv,
		"prometheus.get_label_values",
		operations.TypedRequest[operations.DatasourceLabelArgs]{Args: operations.DatasourceLabelArgs{
			Datasource: "metrics",
			Label:      "job/name",
		}},
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("prometheus.get_label_values status = %d, want 200", rec.Code)
	}
}

func TestPrometheusHandlersRangeQueryAndInstantValidation(t *testing.T) {
	t.Parallel()

	requests := make(chan *http.Request, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Clone(r.Context())
		_, _ = w.Write([]byte(`{"status":"success","data":[]}`))
	}))
	defer upstream.Close()

	srv := newOperationTestService(&stubProxyService{url: upstream.URL}, nil, upstream.Client())

	rec := performOperationRequest(
		t,
		srv,
		"prometheus.query_range",
		operations.TypedRequest[operations.PrometheusRangeQueryArgs]{Args: operations.PrometheusRangeQueryArgs{
			Datasource: "metrics",
			Query:      "up",
			Start:      "2025-01-02T03:04:05Z",
			End:        "2025-01-02T03:14:05Z",
			Step:       "30s",
		}},
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("prometheus.query_range status = %d, want 200", rec.Code)
	}

	req := <-requests
	if req.URL.Path != "/prometheus/api/v1/query_range" {
		t.Fatalf("query_range path = %q, want query_range endpoint", req.URL.Path)
	}
	if req.URL.Query().Get("step") != "30" {
		t.Fatalf("step = %q, want 30", req.URL.Query().Get("step"))
	}

	rec = performOperationRequest(
		t,
		srv,
		"prometheus.query",
		operations.TypedRequest[operations.PrometheusQueryArgs]{Args: operations.PrometheusQueryArgs{
			Datasource: "metrics",
			Query:      "up",
			Time:       "not-a-time",
		}},
	)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "cannot parse time") {
		t.Fatalf("invalid prometheus time = (%d, %q), want parse error", rec.Code, rec.Body.String())
	}
}
