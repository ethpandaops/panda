package transport

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClickHouseHandlerConfiguresSecureDatasource(t *testing.T) {
	t.Parallel()

	handler := NewClickHouseHandler(testLogger(), []ClickHouseDatasourceConfig{{
		Name:       "analytics",
		Host:       "secure-clickhouse.internal",
		Port:       9440,
		Secure:     true,
		SkipVerify: true,
	}})

	datasource := handler.datasources["analytics"]
	if datasource == nil {
		t.Fatal("datasource = nil, want configured datasource")
	}

	transport, ok := datasource.proxy.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("proxy transport = %T, want *http.Transport", datasource.proxy.Transport)
	}

	if transport.TLSClientConfig == nil || !transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatalf("TLSClientConfig = %#v, want skip verify enabled", transport.TLSClientConfig)
	}

	datasource.proxy.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Scheme != "https" || req.URL.Host != "secure-clickhouse.internal:9440" {
			t.Fatalf("upstream target = %s://%s, want https://secure-clickhouse.internal:9440", req.URL.Scheme, req.URL.Host)
		}
		if got := req.URL.Query().Get("database"); got != "" {
			t.Fatalf("database query = %q, want empty", got)
		}

		return httpResponse(http.StatusNoContent, "", nil), nil
	})

	req := httptest.NewRequest(http.MethodGet, "http://proxy.test/clickhouse", nil)
	req.Header.Set(DatasourceHeader, "analytics")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}
