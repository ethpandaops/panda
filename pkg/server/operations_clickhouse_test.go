package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethpandaops/panda/pkg/operations"
)

func TestClickHouseQueryOperationValidatesInputsAndUpstreamFailures(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "clickhouse unavailable", http.StatusBadGateway)
	}))
	defer upstream.Close()

	srv := newOperationTestService(&stubProxyService{url: upstream.URL}, nil, upstream.Client())

	rec := performOperationRequest(
		t,
		srv,
		"clickhouse.query",
		operations.TypedRequest[operations.ClickHouseQueryArgs]{Args: operations.ClickHouseQueryArgs{
			Datasource: "xatu",
		}},
	)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "sql is required") {
		t.Fatalf("missing SQL = (%d, %q), want sql is required", rec.Code, rec.Body.String())
	}

	rec = performOperationRequest(
		t,
		srv,
		"clickhouse.query_raw",
		operations.TypedRequest[operations.ClickHouseQueryArgs]{Args: operations.ClickHouseQueryArgs{
			Datasource: "xatu",
			SQL:        "SELECT 1",
		}},
	)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "clickhouse.query_raw upstream failure") {
		t.Fatalf("upstream failure = (%d, %q), want wrapped upstream error", rec.Code, rec.Body.String())
	}
}

func TestClickHouseQueryOperationSupportsClusterFallbackAndParameters(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Datasource"); got != "archive" {
			t.Fatalf("X-Datasource = %q, want archive", got)
		}
		if got := r.URL.Query().Get("param_enabled"); got != "1" {
			t.Fatalf("param_enabled = %q, want 1", got)
		}
		if got := r.URL.Query().Get("param_limit"); got != "5" {
			t.Fatalf("param_limit = %q, want 5", got)
		}

		_, _ = w.Write([]byte("tsv"))
	}))
	defer upstream.Close()

	srv := newOperationTestService(&stubProxyService{url: upstream.URL}, nil, upstream.Client())

	rec := performOperationRequest(
		t,
		srv,
		"clickhouse.query",
		operations.TypedRequest[operations.ClickHouseQueryArgs]{Args: operations.ClickHouseQueryArgs{
			Cluster: "archive",
			SQL:     "SELECT 1",
			Parameters: map[string]any{
				"enabled": true,
				"limit":   5,
			},
		}},
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("cluster fallback status = %d, want 200", rec.Code)
	}
	if rec.Header().Get("X-Operation-Transport") != "passthrough" {
		t.Fatalf("X-Operation-Transport = %q, want passthrough", rec.Header().Get("X-Operation-Transport"))
	}
}

func TestClickHouseQueryOperationRequiresDatasourceOrCluster(t *testing.T) {
	t.Parallel()

	srv := newOperationTestService(&stubProxyService{url: "https://proxy.example"}, nil, http.DefaultClient)

	rec := performOperationRequest(
		t,
		srv,
		"clickhouse.query",
		operations.TypedRequest[operations.ClickHouseQueryArgs]{Args: operations.ClickHouseQueryArgs{
			SQL: "SELECT 1",
		}},
	)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "datasource or cluster is required") {
		t.Fatalf("missing datasource/cluster = (%d, %q), want required error", rec.Code, rec.Body.String())
	}
}
