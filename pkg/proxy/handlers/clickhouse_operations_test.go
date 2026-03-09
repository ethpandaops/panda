package handlers

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/mcp/pkg/operations"
)

func TestClickHouseOperationsQuery(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("default_format"); got != "JSON" {
			t.Fatalf("unexpected format: %s", got)
		}
		_, _ = w.Write([]byte(`{"meta":[{"name":"value","type":"UInt8"}],"data":[{"value":1}],"rows":1}`))
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

	var response operations.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if response.Kind != operations.ResultKindTable {
		t.Fatalf("unexpected kind: %s", response.Kind)
	}
	if response.RowEncoding != operations.RowEncodingObject {
		t.Fatalf("unexpected row encoding: %s", response.RowEncoding)
	}
	if len(response.Rows) != 1 || response.Rows[0]["value"] != float64(1) {
		t.Fatalf("unexpected rows: %#v", response.Rows)
	}
}

func TestClickHouseOperationsQueryRaw(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("default_format"); got != "JSONCompact" {
			t.Fatalf("unexpected format: %s", got)
		}
		_, _ = w.Write([]byte(`{"meta":[{"name":"value","type":"UInt8"}],"data":[[1],[2]],"rows":2}`))
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

	var response operations.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if response.RowEncoding != operations.RowEncodingArray {
		t.Fatalf("unexpected row encoding: %s", response.RowEncoding)
	}
	if len(response.Matrix) != 2 || len(response.Matrix[0]) != 1 {
		t.Fatalf("unexpected matrix: %#v", response.Matrix)
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
