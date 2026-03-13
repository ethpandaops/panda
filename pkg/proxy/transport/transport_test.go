package transport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/sirupsen/logrus"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type failingCredentialsProvider struct {
	err error
}

func (p failingCredentialsProvider) Retrieve(context.Context) (aws.Credentials, error) {
	return aws.Credentials{}, p.err
}

type errorReadCloser struct {
	err error
}

func (r errorReadCloser) Read([]byte) (int, error) {
	return 0, r.err
}

func (r errorReadCloser) Close() error {
	return nil
}

func testLogger() logrus.FieldLogger {
	logger := logrus.New()
	logger.SetOutput(io.Discard)

	return logger
}

func httpResponse(status int, body string, headers map[string]string) *http.Response {
	header := make(http.Header, len(headers))
	for key, value := range headers {
		header.Set(key, value)
	}

	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestClickHouseHandlerServeHTTP(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		requestTarget  string
		requestBody    string
		expectedPath   string
		expectedDB     string
		expectedFoo    string
		expectDeadline bool
	}{
		{
			name:           "adds default database and strips prefix",
			requestTarget:  "http://proxy.test/clickhouse/query?foo=bar",
			requestBody:    "SELECT 1",
			expectedPath:   "/query",
			expectedDB:     "analytics",
			expectedFoo:    "bar",
			expectDeadline: true,
		},
		{
			name:           "preserves explicit database and rewrites root path",
			requestTarget:  "http://proxy.test/clickhouse?database=override",
			requestBody:    "SELECT 2",
			expectedPath:   "/",
			expectedDB:     "override",
			expectDeadline: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			handler := NewClickHouseHandler(testLogger(), []ClickHouseDatasourceConfig{{
				Name:       "analytics",
				Host:       "clickhouse.internal",
				Port:       8123,
				Database:   "analytics",
				Username:   "user",
				Password:   "pass",
				Secure:     false,
				Timeout:    1,
				SkipVerify: true,
			}})

			var sawDeadline bool
			handler.datasources["analytics"].proxy.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Scheme != "http" {
					t.Fatalf("request scheme = %q, want %q", req.URL.Scheme, "http")
				}
				if req.URL.Host != "clickhouse.internal:8123" {
					t.Fatalf("request host = %q, want %q", req.URL.Host, "clickhouse.internal:8123")
				}
				if req.URL.Path != tc.expectedPath {
					t.Fatalf("request path = %q, want %q", req.URL.Path, tc.expectedPath)
				}
				if got := req.URL.Query().Get("database"); got != tc.expectedDB {
					t.Fatalf("database query = %q, want %q", got, tc.expectedDB)
				}
				if got := req.URL.Query().Get("foo"); got != tc.expectedFoo {
					t.Fatalf("foo query = %q, want %q", got, tc.expectedFoo)
				}
				if got := req.Host; got != "clickhouse.internal:8123" {
					t.Fatalf("request host header = %q, want %q", got, "clickhouse.internal:8123")
				}
				if got := req.Header.Get("Authorization"); strings.HasPrefix(got, "Bearer ") {
					t.Fatalf("authorization header still has bearer token: %q", got)
				}
				username, password, ok := req.BasicAuth()
				if !ok || username != "user" || password != "pass" {
					t.Fatalf("BasicAuth() = (%q, %q, %v), want (%q, %q, true)", username, password, ok, "user", "pass")
				}
				if body, err := io.ReadAll(req.Body); err != nil {
					t.Fatalf("ReadAll(request body) error = %v", err)
				} else if string(body) != tc.requestBody {
					t.Fatalf("request body = %q, want %q", string(body), tc.requestBody)
				}

				_, sawDeadline = req.Context().Deadline()

				return httpResponse(http.StatusAccepted, "ok", map[string]string{"X-Upstream": "clickhouse"}), nil
			})

			req := httptest.NewRequest(http.MethodPost, tc.requestTarget, strings.NewReader(tc.requestBody))
			req.Header.Set(DatasourceHeader, "analytics")
			req.Header.Set("Authorization", "Bearer sandbox-token")

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusAccepted {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
			}
			if body := rec.Body.String(); body != "ok" {
				t.Fatalf("response body = %q, want %q", body, "ok")
			}
			if got := rec.Header().Get("X-Upstream"); got != "clickhouse" {
				t.Fatalf("response header X-Upstream = %q, want %q", got, "clickhouse")
			}
			if sawDeadline != tc.expectDeadline {
				t.Fatalf("deadline present = %v, want %v", sawDeadline, tc.expectDeadline)
			}
		})

	}
}

func TestClickHouseHandlerErrorsAndDatasources(t *testing.T) {
	t.Parallel()

	t.Run("missing and unknown datasource headers", func(t *testing.T) {
		t.Parallel()

		handler := NewClickHouseHandler(testLogger(), []ClickHouseDatasourceConfig{{
			Name:   "analytics",
			Host:   "clickhouse.internal",
			Port:   8123,
			Secure: false,
		}})

		cases := []struct {
			name       string
			header     string
			wantStatus int
			wantBody   string
		}{
			{
				name:       "missing header",
				wantStatus: http.StatusBadRequest,
				wantBody:   "missing X-Datasource header",
			},
			{
				name:       "unknown header",
				header:     "missing",
				wantStatus: http.StatusNotFound,
				wantBody:   "unknown datasource: missing",
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodGet, "http://proxy.test/clickhouse", nil)
				if tc.header != "" {
					req.Header.Set(DatasourceHeader, tc.header)
				}

				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)

				if rec.Code != tc.wantStatus {
					t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
				}
				if !strings.Contains(rec.Body.String(), tc.wantBody) {
					t.Fatalf("body = %q, want substring %q", rec.Body.String(), tc.wantBody)
				}
			})
		}
	})

	t.Run("proxy errors are surfaced as bad gateway", func(t *testing.T) {
		t.Parallel()

		handler := NewClickHouseHandler(testLogger(), []ClickHouseDatasourceConfig{{
			Name:   "analytics",
			Host:   "clickhouse.internal",
			Port:   8123,
			Secure: false,
		}})
		handler.datasources["analytics"].proxy.Transport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial failed")
		})

		req := httptest.NewRequest(http.MethodGet, "http://proxy.test/clickhouse", nil)
		req.Header.Set(DatasourceHeader, "analytics")

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadGateway)
		}
		if !strings.Contains(rec.Body.String(), "proxy error: dial failed") {
			t.Fatalf("body = %q, want proxy error", rec.Body.String())
		}
	})

	t.Run("datasource aliases expose configured names", func(t *testing.T) {
		t.Parallel()

		handler := NewClickHouseHandler(testLogger(), []ClickHouseDatasourceConfig{
			{Name: "alpha", Host: "a", Port: 8123, Secure: false},
			{Name: "beta", Host: "b", Port: 8123, Secure: false},
		})

		names := handler.Datasources()
		if len(names) != 2 {
			t.Fatalf("Datasources() len = %d, want 2", len(names))
		}

		clusters := handler.Clusters()
		if len(clusters) != 2 {
			t.Fatalf("Clusters() len = %d, want 2", len(clusters))
		}
	})
}

func TestLokiHandler(t *testing.T) {
	t.Parallel()

	t.Run("constructor rejects invalid URLs", func(t *testing.T) {
		t.Parallel()

		_, err := NewLokiHandler(testLogger(), []LokiConfig{{Name: "bad", URL: "://bad"}})
		if err == nil || !strings.Contains(err.Error(), `instance "bad"`) {
			t.Fatalf("NewLokiHandler() error = %v, want instance context", err)
		}
	})

	t.Run("parseTargetURL trims input and validates scheme+host", func(t *testing.T) {
		t.Parallel()

		parsed, err := parseTargetURL(" https://logs.example.com/base ")
		if err != nil {
			t.Fatalf("parseTargetURL() unexpected error: %v", err)
		}
		if parsed.Scheme != "https" || parsed.Host != "logs.example.com" {
			t.Fatalf("parsed URL = %s://%s, want https://logs.example.com", parsed.Scheme, parsed.Host)
		}

		if _, err := parseTargetURL("logs.example.com"); err == nil {
			t.Fatal("parseTargetURL() error = nil, want invalid URL error")
		}
	})

	t.Run("servehttp rewrites path and applies basic auth", func(t *testing.T) {
		t.Parallel()

		handler, err := NewLokiHandler(testLogger(), []LokiConfig{{
			Name:       "logs",
			URL:        "https://logs.example.com",
			Username:   "user",
			Password:   "pass",
			SkipVerify: true,
			Timeout:    1,
		}})
		if err != nil {
			t.Fatalf("NewLokiHandler() unexpected error: %v", err)
		}

		var sawDeadline bool
		handler.instances["logs"].proxy.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Scheme != "https" || req.URL.Host != "logs.example.com" {
				t.Fatalf("upstream target = %s://%s, want https://logs.example.com", req.URL.Scheme, req.URL.Host)
			}
			if req.URL.Path != "/api/v1/query_range" {
				t.Fatalf("request path = %q, want %q", req.URL.Path, "/api/v1/query_range")
			}
			if got := req.URL.Query().Get("limit"); got != "5" {
				t.Fatalf("limit query = %q, want %q", got, "5")
			}
			username, password, ok := req.BasicAuth()
			if !ok || username != "user" || password != "pass" {
				t.Fatalf("BasicAuth() = (%q, %q, %v), want (%q, %q, true)", username, password, ok, "user", "pass")
			}
			if got := req.Header.Get("Authorization"); strings.HasPrefix(got, "Bearer ") {
				t.Fatalf("authorization header still has bearer token: %q", got)
			}
			if got := req.Host; got != "logs.example.com" {
				t.Fatalf("request host = %q, want %q", got, "logs.example.com")
			}

			_, sawDeadline = req.Context().Deadline()

			return httpResponse(http.StatusOK, "loki-ok", nil), nil
		})

		req := httptest.NewRequest(http.MethodGet, "http://proxy.test/loki/api/v1/query_range?limit=5", nil)
		req.Header.Set(DatasourceHeader, "logs")
		req.Header.Set("Authorization", "Bearer sandbox-token")

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if rec.Body.String() != "loki-ok" {
			t.Fatalf("body = %q, want %q", rec.Body.String(), "loki-ok")
		}
		if !sawDeadline {
			t.Fatal("request context deadline not applied")
		}
	})

	t.Run("missing and unknown datasource headers", func(t *testing.T) {
		t.Parallel()

		handler, err := NewLokiHandler(testLogger(), []LokiConfig{{Name: "logs", URL: "https://logs.example.com"}})
		if err != nil {
			t.Fatalf("NewLokiHandler() unexpected error: %v", err)
		}

		cases := []struct {
			name       string
			header     string
			wantStatus int
		}{
			{name: "missing", wantStatus: http.StatusBadRequest},
			{name: "unknown", header: "missing", wantStatus: http.StatusNotFound},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodGet, "http://proxy.test/loki", nil)
				if tc.header != "" {
					req.Header.Set(DatasourceHeader, tc.header)
				}
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				if rec.Code != tc.wantStatus {
					t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
				}
			})
		}
	})

	t.Run("proxy transport errors return bad gateway", func(t *testing.T) {
		t.Parallel()

		handler, err := NewLokiHandler(testLogger(), []LokiConfig{{Name: "logs", URL: "https://logs.example.com"}})
		if err != nil {
			t.Fatalf("NewLokiHandler() unexpected error: %v", err)
		}
		handler.instances["logs"].proxy.Transport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("loki unavailable")
		})

		req := httptest.NewRequest(http.MethodGet, "http://proxy.test/loki/api/v1/query", nil)
		req.Header.Set(DatasourceHeader, "logs")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadGateway)
		}
		if !strings.Contains(rec.Body.String(), "proxy error: loki unavailable") {
			t.Fatalf("body = %q, want proxy error", rec.Body.String())
		}
	})
}

func TestPrometheusHandler(t *testing.T) {
	t.Parallel()

	t.Run("servehttp rewrites path and applies auth", func(t *testing.T) {
		t.Parallel()

		handler := NewPrometheusHandler(testLogger(), []PrometheusConfig{{
			Name:     "metrics",
			URL:      "https://prom.example.com",
			Username: "user",
			Password: "pass",
			Timeout:  1,
		}})

		var sawDeadline bool
		handler.instances["metrics"].proxy.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Scheme != "https" || req.URL.Host != "prom.example.com" {
				t.Fatalf("upstream target = %s://%s, want https://prom.example.com", req.URL.Scheme, req.URL.Host)
			}
			if req.URL.Path != "/api/v1/query" {
				t.Fatalf("request path = %q, want %q", req.URL.Path, "/api/v1/query")
			}
			if got := req.URL.Query().Get("query"); got != "up" {
				t.Fatalf("query param = %q, want %q", got, "up")
			}
			username, password, ok := req.BasicAuth()
			if !ok || username != "user" || password != "pass" {
				t.Fatalf("BasicAuth() = (%q, %q, %v), want (%q, %q, true)", username, password, ok, "user", "pass")
			}
			if got := req.Host; got != "prom.example.com" {
				t.Fatalf("request host = %q, want %q", got, "prom.example.com")
			}
			if got := req.Header.Get("Authorization"); strings.HasPrefix(got, "Bearer ") {
				t.Fatalf("authorization header still has bearer token: %q", got)
			}

			_, sawDeadline = req.Context().Deadline()

			return httpResponse(http.StatusOK, "prom-ok", nil), nil
		})

		req := httptest.NewRequest(http.MethodGet, "http://proxy.test/prometheus/api/v1/query?query=up", nil)
		req.Header.Set(DatasourceHeader, "metrics")
		req.Header.Set("Authorization", "Bearer sandbox-token")

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if rec.Body.String() != "prom-ok" {
			t.Fatalf("body = %q, want %q", rec.Body.String(), "prom-ok")
		}
		if !sawDeadline {
			t.Fatal("request context deadline not applied")
		}
	})

	t.Run("missing unknown and improperly configured instances", func(t *testing.T) {
		t.Parallel()

		handler := NewPrometheusHandler(testLogger(), []PrometheusConfig{
			{Name: "metrics", URL: "https://prom.example.com"},
			{Name: "broken", URL: "://broken"},
		})

		cases := []struct {
			name       string
			header     string
			wantStatus int
			wantBody   string
		}{
			{
				name:       "missing",
				wantStatus: http.StatusBadRequest,
				wantBody:   "missing X-Datasource header",
			},
			{
				name:       "unknown",
				header:     "missing",
				wantStatus: http.StatusNotFound,
				wantBody:   "unknown instance: missing",
			},
			{
				name:       "invalid config yields nil instance",
				header:     "broken",
				wantStatus: http.StatusInternalServerError,
				wantBody:   "instance broken not properly configured",
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodGet, "http://proxy.test/prometheus", nil)
				if tc.header != "" {
					req.Header.Set(DatasourceHeader, tc.header)
				}

				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)

				if rec.Code != tc.wantStatus {
					t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
				}
				if !strings.Contains(rec.Body.String(), tc.wantBody) {
					t.Fatalf("body = %q, want substring %q", rec.Body.String(), tc.wantBody)
				}
			})
		}

		if got := handler.Instances(); len(got) != 2 {
			t.Fatalf("Instances() len = %d, want 2", len(got))
		}
	})

	t.Run("proxy transport errors return bad gateway", func(t *testing.T) {
		t.Parallel()

		handler := NewPrometheusHandler(testLogger(), []PrometheusConfig{{Name: "metrics", URL: "https://prom.example.com"}})
		handler.instances["metrics"].proxy.Transport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("prometheus unavailable")
		})

		req := httptest.NewRequest(http.MethodGet, "http://proxy.test/prometheus/api/v1/query", nil)
		req.Header.Set(DatasourceHeader, "metrics")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadGateway)
		}
		if !strings.Contains(rec.Body.String(), "proxy error: prometheus unavailable") {
			t.Fatalf("body = %q, want proxy error", rec.Body.String())
		}
	})
}

func TestEthNodeHandler(t *testing.T) {
	t.Parallel()

	t.Run("validates incoming paths", func(t *testing.T) {
		t.Parallel()

		handler := NewEthNodeHandler(testLogger(), EthNodeConfig{})
		cases := []struct {
			name       string
			path       string
			wantStatus int
			wantBody   string
		}{
			{
				name:       "invalid prefix",
				path:       "/invalid/main/one",
				wantStatus: http.StatusBadRequest,
				wantBody:   "invalid path: must start with /beacon/ or /execution/",
			},
			{
				name:       "missing instance segment",
				path:       "/beacon/mainnet",
				wantStatus: http.StatusBadRequest,
				wantBody:   "invalid path: must include /{network}/{instance}/...",
			},
			{
				name:       "invalid network segment",
				path:       "/beacon/Mainnet/one/rest",
				wantStatus: http.StatusBadRequest,
				wantBody:   "invalid network name",
			},
			{
				name:       "invalid instance segment",
				path:       "/execution/mainnet/node_1/rest",
				wantStatus: http.StatusBadRequest,
				wantBody:   "invalid instance name",
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodGet, "http://proxy.test"+tc.path, nil)
				rec := httptest.NewRecorder()

				handler.ServeHTTP(rec, req)

				if rec.Code != tc.wantStatus {
					t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
				}
				if !strings.Contains(rec.Body.String(), tc.wantBody) {
					t.Fatalf("body = %q, want substring %q", rec.Body.String(), tc.wantBody)
				}
			})
		}
	})

	t.Run("rewrites beacon and execution requests and reuses cached proxies", func(t *testing.T) {
		t.Parallel()

		handler := NewEthNodeHandler(testLogger(), EthNodeConfig{Username: "user", Password: "pass"})

		cases := []struct {
			name         string
			path         string
			expectedHost string
			expectedPath string
		}{
			{
				name:         "beacon request",
				path:         "/beacon/hoodi/main/eth/v1/node/version",
				expectedHost: "bn-main.srv.hoodi.ethpandaops.io",
				expectedPath: "/eth/v1/node/version",
			},
			{
				name:         "execution root request",
				path:         "/execution/hoodi/main",
				expectedHost: "rpc-main.srv.hoodi.ethpandaops.io",
				expectedPath: "/",
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				proxy := handler.getOrCreateProxy(tc.expectedHost)
				if again := handler.getOrCreateProxy(tc.expectedHost); again != proxy {
					t.Fatal("getOrCreateProxy() did not return cached proxy")
				}

				proxy.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
					if req.URL.Scheme != "https" || req.URL.Host != tc.expectedHost {
						t.Fatalf("upstream target = %s://%s, want https://%s", req.URL.Scheme, req.URL.Host, tc.expectedHost)
					}
					if req.URL.Path != tc.expectedPath {
						t.Fatalf("request path = %q, want %q", req.URL.Path, tc.expectedPath)
					}
					if got := req.Host; got != tc.expectedHost {
						t.Fatalf("request host = %q, want %q", got, tc.expectedHost)
					}
					if got := req.Header.Get("Authorization"); strings.HasPrefix(got, "Bearer ") {
						t.Fatalf("authorization header still has bearer token: %q", got)
					}
					username, password, ok := req.BasicAuth()
					if !ok || username != "user" || password != "pass" {
						t.Fatalf("BasicAuth() = (%q, %q, %v), want (%q, %q, true)", username, password, ok, "user", "pass")
					}

					return httpResponse(http.StatusOK, "eth-ok", nil), nil
				})

				req := httptest.NewRequest(http.MethodGet, "http://proxy.test"+tc.path, nil)
				req.Header.Set("Authorization", "Bearer sandbox-token")
				rec := httptest.NewRecorder()

				handler.ServeHTTP(rec, req)

				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
				}
				if rec.Body.String() != "eth-ok" {
					t.Fatalf("body = %q, want %q", rec.Body.String(), "eth-ok")
				}
			})
		}
	})

	t.Run("proxy transport errors return bad gateway", func(t *testing.T) {
		t.Parallel()

		handler := NewEthNodeHandler(testLogger(), EthNodeConfig{})
		proxy := handler.getOrCreateProxy("bn-main.srv.hoodi.ethpandaops.io")
		proxy.Transport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("ethnode unavailable")
		})

		req := httptest.NewRequest(http.MethodGet, "http://proxy.test/beacon/hoodi/main/eth/v1/node/version", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadGateway)
		}
		if !strings.Contains(rec.Body.String(), "proxy error: ethnode unavailable") {
			t.Fatalf("body = %q, want proxy error", rec.Body.String())
		}
	})
}

func TestS3HandlerServeHTTP(t *testing.T) {
	t.Parallel()

	emptyHash := sha256.Sum256(nil)
	payloadHash := sha256.Sum256([]byte("payload"))

	cases := []struct {
		name             string
		method           string
		target           string
		body             string
		headers          map[string]string
		expectedPath     string
		expectedQuery    string
		expectedBody     string
		expectedHash     string
		expectedLength   int64
		expectedRespBody string
		expectedRespCode int
	}{
		{
			name:             "signed get request copies query and headers",
			method:           http.MethodGet,
			target:           "http://proxy.test/s3/artifacts/reports/output.txt?download=1",
			expectedPath:     "/artifacts/reports/output.txt",
			expectedQuery:    "download=1",
			expectedHash:     hex.EncodeToString(emptyHash[:]),
			expectedLength:   0,
			expectedRespBody: "downloaded",
			expectedRespCode: http.StatusOK,
		},
		{
			name:   "signed put request forwards body and selected headers",
			method: http.MethodPut,
			target: "http://proxy.test/s3/artifacts/reports/output.txt?partNumber=1",
			body:   "payload",
			headers: map[string]string{
				"Content-Type": "text/plain",
				"Content-MD5":  "abc123",
			},
			expectedPath:     "/artifacts/reports/output.txt",
			expectedQuery:    "partNumber=1",
			expectedBody:     "payload",
			expectedHash:     hex.EncodeToString(payloadHash[:]),
			expectedLength:   int64(len("payload")),
			expectedRespBody: "uploaded",
			expectedRespCode: http.StatusCreated,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			handler := NewS3Handler(testLogger(), &S3Config{
				Endpoint:        "https://s3.example.com",
				AccessKey:       "access-key",
				SecretKey:       "secret-key",
				Bucket:          "artifacts",
				Region:          "us-east-1",
				PublicURLPrefix: "https://cdn.example.com",
			})

			handler.httpClient = &http.Client{
				Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
					if req.URL.Scheme != "https" || req.URL.Host != "s3.example.com" {
						t.Fatalf("upstream target = %s://%s, want https://s3.example.com", req.URL.Scheme, req.URL.Host)
					}
					if req.URL.Path != tc.expectedPath {
						t.Fatalf("request path = %q, want %q", req.URL.Path, tc.expectedPath)
					}
					if req.URL.RawQuery != tc.expectedQuery {
						t.Fatalf("request query = %q, want %q", req.URL.RawQuery, tc.expectedQuery)
					}
					if got := req.Header.Get("X-Amz-Content-Sha256"); got != tc.expectedHash {
						t.Fatalf("X-Amz-Content-Sha256 = %q, want %q", got, tc.expectedHash)
					}
					if req.ContentLength != tc.expectedLength {
						t.Fatalf("ContentLength = %d, want %d", req.ContentLength, tc.expectedLength)
					}
					if got := req.Header.Get("Authorization"); got == "" {
						t.Fatal("Authorization header not set by signer")
					}
					for key, value := range tc.headers {
						if got := req.Header.Get(key); got != value {
							t.Fatalf("%s header = %q, want %q", key, got, value)
						}
					}

					if req.Body == nil {
						if tc.expectedBody != "" {
							t.Fatalf("request body = nil, want %q", tc.expectedBody)
						}
					} else {
						var requestBody string
						if req.Body != nil {
							body, err := io.ReadAll(req.Body)
							if err != nil {
								t.Fatalf("ReadAll(request body) error = %v", err)
							}

							requestBody = string(body)
						}
						if requestBody != tc.expectedBody {
							t.Fatalf("request body = %q, want %q", requestBody, tc.expectedBody)
						}
					}

					return httpResponse(tc.expectedRespCode, tc.expectedRespBody, map[string]string{"ETag": "etag-1"}), nil
				}),
			}

			var body io.Reader
			if tc.body != "" {
				body = bytes.NewBufferString(tc.body)
			}

			req := httptest.NewRequest(tc.method, tc.target, body)
			for key, value := range tc.headers {
				req.Header.Set(key, value)
			}

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tc.expectedRespCode {
				t.Fatalf("status = %d, want %d", rec.Code, tc.expectedRespCode)
			}
			if rec.Body.String() != tc.expectedRespBody {
				t.Fatalf("body = %q, want %q", rec.Body.String(), tc.expectedRespBody)
			}
			if got := rec.Header().Get("ETag"); got != "etag-1" {
				t.Fatalf("ETag header = %q, want %q", got, "etag-1")
			}
		})
	}
}

func TestS3HandlerErrorsAndHelpers(t *testing.T) {
	t.Parallel()

	t.Run("constructor returns nil for nil config", func(t *testing.T) {
		t.Parallel()

		if handler := NewS3Handler(testLogger(), nil); handler != nil {
			t.Fatalf("NewS3Handler(nil) = %#v, want nil", handler)
		}
	})

	t.Run("handles missing config and bucket path", func(t *testing.T) {
		t.Parallel()

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://proxy.test/s3/artifacts/file.txt", nil)
		(&S3Handler{log: testLogger()}).ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
		}

		handler := NewS3Handler(testLogger(), &S3Config{
			Endpoint:  "https://s3.example.com",
			AccessKey: "access-key",
			SecretKey: "secret-key",
			Bucket:    "artifacts",
			Region:    "us-east-1",
		})

		rec = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodGet, "http://proxy.test/s3/", nil)
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("surfaces credential and transport failures", func(t *testing.T) {
		t.Parallel()

		handler := NewS3Handler(testLogger(), &S3Config{
			Endpoint:  "https://s3.example.com",
			AccessKey: "access-key",
			SecretKey: "secret-key",
			Bucket:    "artifacts",
			Region:    "us-east-1",
		})

		handler.credentials = failingCredentialsProvider{err: errors.New("creds unavailable")}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://proxy.test/s3/artifacts/file.txt", nil)
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
		}
		if !strings.Contains(rec.Body.String(), "failed to retrieve credentials") {
			t.Fatalf("body = %q, want credential failure", rec.Body.String())
		}

		handler = NewS3Handler(testLogger(), &S3Config{
			Endpoint:        "https://s3.example.com",
			AccessKey:       "access-key",
			SecretKey:       "secret-key",
			Bucket:          "artifacts",
			Region:          "us-east-1",
			PublicURLPrefix: "https://cdn.example.com/static",
		})
		handler.httpClient = &http.Client{
			Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("upstream down")
			}),
		}

		rec = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodGet, "http://proxy.test/s3/artifacts/file.txt", nil)
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadGateway)
		}
		if !strings.Contains(rec.Body.String(), "failed to execute request") {
			t.Fatalf("body = %q, want execute failure", rec.Body.String())
		}

		if got := handler.GetPublicURL(context.Background(), "artifacts", "file.txt"); got != "https://cdn.example.com/static/file.txt" {
			t.Fatalf("GetPublicURL() = %q, want %q", got, "https://cdn.example.com/static/file.txt")
		}
		if got := handler.Bucket(); got != "artifacts" {
			t.Fatalf("Bucket() = %q, want %q", got, "artifacts")
		}
		if got := handler.PublicURLPrefix(); got != "https://cdn.example.com/static" {
			t.Fatalf("PublicURLPrefix() = %q, want %q", got, "https://cdn.example.com/static")
		}

		handler.publicURLPrefix = ""
		if got := handler.GetPublicURL(context.Background(), "artifacts", "file.txt"); got != "https://s3.example.com/artifacts/file.txt" {
			t.Fatalf("GetPublicURL() fallback = %q, want %q", got, "https://s3.example.com/artifacts/file.txt")
		}
	})

	t.Run("returns empty bucket when config is absent", func(t *testing.T) {
		t.Parallel()

		if got := (&S3Handler{}).Bucket(); got != "" {
			t.Fatalf("Bucket() = %q, want empty string", got)
		}
	})

	t.Run("surfaces body read and request creation failures", func(t *testing.T) {
		t.Parallel()

		handler := NewS3Handler(testLogger(), &S3Config{
			Endpoint:  "https://s3.example.com",
			AccessKey: "access-key",
			SecretKey: "secret-key",
			Bucket:    "artifacts",
			Region:    "us-east-1",
		})

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "http://proxy.test/s3/artifacts/file.txt", nil)
		req.Body = errorReadCloser{err: errors.New("read failed")}
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
		}
		if !strings.Contains(rec.Body.String(), "failed to read request body: read failed") {
			t.Fatalf("body = %q, want body read failure", rec.Body.String())
		}

		handler = NewS3Handler(testLogger(), &S3Config{
			Endpoint:  "https://s3.example.com\r\nbad",
			AccessKey: "access-key",
			SecretKey: "secret-key",
			Bucket:    "artifacts",
			Region:    "us-east-1",
		})

		rec = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodGet, "http://proxy.test/s3/artifacts/file.txt", nil)
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
		}
		if !strings.Contains(rec.Body.String(), "failed to create request") {
			t.Fatalf("body = %q, want create request failure", rec.Body.String())
		}
	})
}

func TestTransportNameListsAreStable(t *testing.T) {
	t.Parallel()

	prom := NewPrometheusHandler(testLogger(), []PrometheusConfig{
		{Name: "alpha", URL: "https://alpha.example.com"},
		{Name: "beta", URL: "https://beta.example.com"},
	})
	loki, err := NewLokiHandler(testLogger(), []LokiConfig{
		{Name: "alpha", URL: "https://alpha.example.com"},
		{Name: "beta", URL: "https://beta.example.com"},
	})
	if err != nil {
		t.Fatalf("NewLokiHandler() unexpected error: %v", err)
	}

	if got := prom.Instances(); len(got) != 2 {
		t.Fatalf("Prometheus Instances() len = %d, want 2", len(got))
	}
	if got := loki.Instances(); len(got) != 2 {
		t.Fatalf("Loki Instances() len = %d, want 2", len(got))
	}

	if !validSegment.MatchString("mainnet") {
		t.Fatal("validSegment should match mainnet")
	}
}
