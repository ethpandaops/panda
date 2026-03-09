package handlers

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/mcp/pkg/operations"
)

func TestClickHouseOperationsQuery(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("default_format"); got != "TabSeparatedWithNames" {
			t.Fatalf("unexpected format: %s", got)
		}
		w.Header().Set("Content-Type", "text/tab-separated-values")
		_, _ = w.Write([]byte("value\n1\n"))
	}))
	defer upstream.Close()

	host, port := splitHostPort(t, upstream.Listener.Addr().String())
	handler := NewClickHouseOperationsHandler(logrus.New(), []ClickHouseConfig{
		{
			Name:     "xatu",
			Host:     host,
			Port:     port,
			Database: "default",
			Timeout:  5,
		},
	})

	body, _ := json.Marshal(operations.Request{
		Args: map[string]any{
			"cluster": "xatu",
			"sql":     "SELECT 1 AS value",
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/operations/clickhouse.query", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "text/tab-separated-values") {
		t.Fatalf("unexpected content-type: %s", rec.Header().Get("Content-Type"))
	}
	if body := rec.Body.String(); body != "value\n1\n" {
		t.Fatalf("unexpected body: %q", body)
	}
}

func TestClickHouseOperationsQueryRaw(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("default_format"); got != "TabSeparatedWithNames" {
			t.Fatalf("unexpected format: %s", got)
		}
		w.Header().Set("Content-Type", "text/tab-separated-values")
		_, _ = w.Write([]byte("value\n1\n2\n"))
	}))
	defer upstream.Close()

	host, port := splitHostPort(t, upstream.Listener.Addr().String())
	handler := NewClickHouseOperationsHandler(logrus.New(), []ClickHouseConfig{
		{
			Name:     "xatu",
			Host:     host,
			Port:     port,
			Database: "default",
			Timeout:  5,
		},
	})

	body, _ := json.Marshal(operations.Request{
		Args: map[string]any{
			"cluster": "xatu",
			"sql":     "SELECT number AS value FROM numbers(2)",
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/operations/clickhouse.query_raw", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "text/tab-separated-values") {
		t.Fatalf("unexpected content-type: %s", rec.Header().Get("Content-Type"))
	}
	if body := rec.Body.String(); body != "value\n1\n2\n" {
		t.Fatalf("unexpected body: %q", body)
	}
}

func TestClickHouseOperationsListDatasources(t *testing.T) {
	handler := NewClickHouseOperationsHandler(logrus.New(), []ClickHouseConfig{
		{Name: "xatu", Description: "Main cluster", Database: "default"},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/operations/clickhouse.list_datasources", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	var response operations.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if response.Kind != operations.ResultKindObject {
		t.Fatalf("unexpected kind: %s", response.Kind)
	}
}

func splitHostPort(t *testing.T, address string) (string, int) {
	t.Helper()

	host, portString, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}

	port, err := strconv.Atoi(portString)
	if err != nil {
		t.Fatalf("atoi port: %v", err)
	}

	return host, port
}
