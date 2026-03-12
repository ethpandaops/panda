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

func TestLokiHandlersValidateArgsAndDefaultQueryFields(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/loki/loki/api/v1/query" {
			t.Fatalf("request path = %q, want instant query path", r.URL.Path)
		}
		if got := r.URL.Query().Get("limit"); got != "100" {
			t.Fatalf("limit = %q, want 100", got)
		}
		if got := r.URL.Query().Get("direction"); got != "backward" {
			t.Fatalf("direction = %q, want backward", got)
		}

		_, _ = w.Write([]byte(`{"status":"success","data":{"result":[]}}`))
	}))
	defer upstream.Close()

	srv := newOperationTestService(&stubProxyService{url: upstream.URL}, nil, upstream.Client())

	rec := performOperationRequest(
		t,
		srv,
		"loki.query_instant",
		operations.TypedRequest[operations.LokiInstantQueryArgs]{Args: operations.LokiInstantQueryArgs{
			Datasource: "logs",
			Query:      `{job="api"}`,
		}},
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("loki.query_instant status = %d, want 200", rec.Code)
	}

	rec = performOperationRequest(
		t,
		srv,
		"loki.get_label_values",
		operations.TypedRequest[operations.LokiLabelValuesArgs]{Args: operations.LokiLabelValuesArgs{
			Datasource: "logs",
			Start:      "not-a-time",
			Label:      "job",
		}},
	)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "cannot parse time") {
		t.Fatalf("invalid loki time = (%d, %q), want parse error", rec.Code, rec.Body.String())
	}
}

func TestLokiHandlersListDatasourcesAndLabels(t *testing.T) {
	t.Parallel()

	srv := newOperationTestService(&coverageProxyService{
		url: "https://proxy.example",
		lokiInfo: []types.DatasourceInfo{{
			Name:        "logs",
			Description: "Application logs",
			Metadata:    map[string]string{"url": "https://logs.example.com"},
		}},
	}, nil, http.DefaultClient)

	rec := httptest.NewRecorder()
	srv.handleLokiListDatasources(rec)
	if rec.Code != http.StatusOK {
		t.Fatalf("handleLokiListDatasources() status = %d, want 200", rec.Code)
	}

	var response operations.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	payload, err := operations.DecodeResponseData[operations.DatasourcesPayload](&response)
	if err != nil {
		t.Fatalf("DecodeResponseData() error = %v", err)
	}
	if len(payload.Datasources) != 1 || payload.Datasources[0].URL != "https://logs.example.com" {
		t.Fatalf("datasources payload = %#v, want logs URL", payload.Datasources)
	}
}

func TestLokiHandlersValidateDatasourceAndLabelPaths(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/loki/loki/api/v1/label/job/name/values" {
			t.Fatalf("request path = %q, want slash-preserving label values path", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":["api"]}`))
	}))
	defer upstream.Close()

	srv := newOperationTestService(&stubProxyService{url: upstream.URL}, nil, upstream.Client())

	rec := performOperationRequest(
		t,
		srv,
		"loki.query",
		operations.TypedRequest[operations.LokiQueryArgs]{Args: operations.LokiQueryArgs{
			Query: `{job="api"}`,
		}},
	)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "datasource is required") {
		t.Fatalf("missing datasource = (%d, %q), want datasource required", rec.Code, rec.Body.String())
	}

	rec = performOperationRequest(
		t,
		srv,
		"loki.get_label_values",
		operations.TypedRequest[operations.LokiLabelValuesArgs]{Args: operations.LokiLabelValuesArgs{
			Datasource: "logs",
			Label:      "job/name",
		}},
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("loki.get_label_values status = %d, want 200", rec.Code)
	}
}

func TestLokiHandlersRangeQueryAndLabelsSuccess(t *testing.T) {
	t.Parallel()

	requests := make(chan *http.Request, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Clone(r.Context())
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer upstream.Close()

	srv := newOperationTestService(&stubProxyService{url: upstream.URL}, nil, upstream.Client())

	rec := performOperationRequest(
		t,
		srv,
		"loki.query",
		operations.TypedRequest[operations.LokiQueryArgs]{Args: operations.LokiQueryArgs{
			Datasource: "logs",
			Query:      `{job="api"}`,
			Start:      "now-5m",
			End:        "now",
			Direction:  "forward",
			Limit:      5,
		}},
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("loki.query status = %d, want 200", rec.Code)
	}

	req := <-requests
	if req.URL.Path != "/loki/loki/api/v1/query_range" {
		t.Fatalf("range query path = %q, want query_range", req.URL.Path)
	}
	if req.URL.Query().Get("direction") != "forward" || req.URL.Query().Get("limit") != "5" {
		t.Fatalf("range query params = %q, want direction=forward limit=5", req.URL.RawQuery)
	}

	rec = performOperationRequest(
		t,
		srv,
		"loki.get_labels",
		operations.TypedRequest[operations.LokiLabelsArgs]{Args: operations.LokiLabelsArgs{
			Datasource: "logs",
			Start:      "now-1m",
			End:        "now",
		}},
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("loki.get_labels status = %d, want 200", rec.Code)
	}

	req = <-requests
	if req.URL.Path != "/loki/loki/api/v1/labels" {
		t.Fatalf("labels path = %q, want labels endpoint", req.URL.Path)
	}
}
