package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/config"
	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/operations"
	"github.com/ethpandaops/panda/pkg/resource"
	"github.com/ethpandaops/panda/pkg/sandbox"
	"github.com/ethpandaops/panda/pkg/serverapi"
	"github.com/ethpandaops/panda/pkg/tokenstore"
	"github.com/ethpandaops/panda/pkg/tool"
	"github.com/ethpandaops/panda/pkg/types"
)

type coverageSandboxService struct {
	name string
}

func (s coverageSandboxService) Start(context.Context) error { return nil }
func (s coverageSandboxService) Stop(context.Context) error  { return nil }
func (s coverageSandboxService) Execute(context.Context, sandbox.ExecuteRequest) (*sandbox.ExecutionResult, error) {
	return nil, nil
}
func (s coverageSandboxService) Name() string { return s.name }
func (s coverageSandboxService) ListSessions(context.Context, string) ([]sandbox.SessionInfo, error) {
	return nil, nil
}
func (s coverageSandboxService) CreateSession(context.Context, string, map[string]string) (*sandbox.CreatedSession, error) {
	return nil, nil
}
func (s coverageSandboxService) DestroySession(context.Context, string, string) error { return nil }
func (s coverageSandboxService) CanCreateSession(context.Context, string) (bool, int, int) {
	return true, 0, 0
}
func (s coverageSandboxService) SessionsEnabled() bool { return true }

type coverageProxyService struct {
	url            string
	clickhouseInfo []types.DatasourceInfo
	prometheusInfo []types.DatasourceInfo
	lokiInfo       []types.DatasourceInfo
}

func (s *coverageProxyService) Start(context.Context) error          { return nil }
func (s *coverageProxyService) Stop(context.Context) error           { return nil }
func (s *coverageProxyService) URL() string                          { return s.url }
func (s *coverageProxyService) AuthorizeRequest(*http.Request) error { return nil }
func (s *coverageProxyService) RegisterToken(string) string          { return "none" }
func (s *coverageProxyService) RevokeToken(string)                   {}
func (s *coverageProxyService) ClickHouseDatasources() []string      { return nil }
func (s *coverageProxyService) ClickHouseDatasourceInfo() []types.DatasourceInfo {
	return s.clickhouseInfo
}
func (s *coverageProxyService) PrometheusDatasources() []string { return nil }
func (s *coverageProxyService) PrometheusDatasourceInfo() []types.DatasourceInfo {
	return s.prometheusInfo
}
func (s *coverageProxyService) LokiDatasources() []string { return nil }
func (s *coverageProxyService) LokiDatasourceInfo() []types.DatasourceInfo {
	return s.lokiInfo
}
func (s *coverageProxyService) S3Bucket() string          { return "" }
func (s *coverageProxyService) S3PublicURLPrefix() string { return "" }
func (s *coverageProxyService) EthNodeAvailable() bool    { return false }
func (s *coverageProxyService) DatasourceInfo() []types.DatasourceInfo {
	infos := appendTypedDatasourceInfo(nil, "clickhouse", s.clickhouseInfo)
	infos = appendTypedDatasourceInfo(infos, "prometheus", s.prometheusInfo)
	infos = appendTypedDatasourceInfo(infos, "loki", s.lokiInfo)
	return infos
}
func (s *coverageProxyService) Datasources() serverapi.DatasourcesResponse {
	return serverapi.DatasourcesResponse{Datasources: s.DatasourceInfo()}
}

func appendTypedDatasourceInfo(dst []types.DatasourceInfo, kind string, infos []types.DatasourceInfo) []types.DatasourceInfo {
	for _, info := range infos {
		if info.Type == "" {
			info.Type = kind
		}

		dst = append(dst, info)
	}

	return dst
}

func TestBuilderHelpersAndBuildFailure(t *testing.T) {
	t.Parallel()

	builder := NewBuilder(logrus.New(), &config.Config{})
	if builder == nil || builder.cfg == nil {
		t.Fatalf("NewBuilder() = %#v, want builder with config", builder)
	}

	_, err := NewBuilder(logrus.New(), &config.Config{
		Sandbox: config.SandboxConfig{Backend: "firecracker"},
	}).Build(context.Background())
	if err == nil || !strings.Contains(err.Error(), "building sandbox: unsupported sandbox backend") {
		t.Fatalf("Build() error = %v, want sandbox backend error", err)
	}

	meta := buildProxyAuthMetadata(nil)
	if meta == nil || meta.Enabled {
		t.Fatalf("buildProxyAuthMetadata(nil) = %#v, want disabled metadata", meta)
	}

	meta = buildProxyAuthMetadata(&config.Config{
		Proxy: config.ProxyConfig{
			URL: "https://proxy.example/",
			Auth: &config.ProxyAuthConfig{
				ClientID: "client-id",
			},
		},
	})
	if !meta.Enabled || meta.IssuerURL != "https://proxy.example" || meta.Resource != "https://proxy.example" {
		t.Fatalf("buildProxyAuthMetadata() = %#v, want trimmed enabled metadata", meta)
	}

	meta = buildProxyAuthMetadata(&config.Config{
		Proxy: config.ProxyConfig{
			URL: "https://proxy.example/",
			Auth: &config.ProxyAuthConfig{
				IssuerURL: "   ",
			},
		},
	})
	if meta.Enabled || meta.ClientID != "" {
		t.Fatalf("buildProxyAuthMetadata(missing client) = %#v, want disabled metadata", meta)
	}

	toolReg := builder.buildToolRegistry(
		coverageSandboxService{name: "stub"},
		nil,
		nil,
		nil,
		nil,
		nil,
	)
	if got := toolReg.List(); len(got) != 2 {
		t.Fatalf("buildToolRegistry() len = %d, want 2 built-in tools", len(got))
	}

	moduleReg := newInitializedModuleRegistry(t)
	resourceReg := builder.buildResourceRegistry(nil, nil, moduleReg, toolReg)
	if got := len(resourceReg.ListStatic()); got != 9 {
		t.Fatalf("buildResourceRegistry() static count = %d, want 9", got)
	}
	if got := len(resourceReg.ListTemplates()); got != 1 {
		t.Fatalf("buildResourceRegistry() template count = %d, want 1", got)
	}
}

func TestServerCoreHelpers(t *testing.T) {
	t.Parallel()

	log := logrus.New()
	toolReg := tool.NewRegistry(log)
	toolReg.Register(tool.Definition{
		Tool: mcp.NewTool("echo"),
		Handler: func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return tool.CallToolSuccess("ok"), nil
		},
	})

	resourceReg := resource.NewRegistry(log)
	resourceReg.RegisterStatic(types.StaticResource{
		Resource: mcp.NewResource("plain://hello", "Hello", mcp.WithMIMEType("text/plain")),
		Handler: func(context.Context, string) (string, error) {
			return "hello", nil
		},
	})
	resourceReg.RegisterTemplate(types.TemplateResource{
		Template: mcp.NewResourceTemplate("templ://{name}", "Template", mcp.WithTemplateMIMEType("text/plain")),
		Pattern:  regexp.MustCompile(`^templ://.+$`),
		Handler: func(_ context.Context, uri string) (string, error) {
			return "template:" + uri, nil
		},
	})

	tokens := tokenstore.New(time.Hour)
	t.Cleanup(tokens.Stop)

	cleanupCalled := false
	srv := NewService(
		log,
		config.ServerConfig{Transport: "invalid"},
		toolReg,
		resourceReg,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		&serverapi.ProxyAuthMetadataResponse{},
		tokens,
		func(context.Context) error {
			cleanupCalled = true
			return nil
		},
	).(*service)

	if err := srv.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "unknown transport") {
		t.Fatalf("Start() error = %v, want unknown transport", err)
	}

	if srv.mcpServer == nil {
		t.Fatal("Start() did not initialize mcpServer")
	}

	if err := srv.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if !cleanupCalled {
		t.Fatal("Stop() did not call cleanup")
	}

	srv.mcpServer = mcpserver.NewMCPServer("test", "v1")

	handler := srv.wrapToolHandler("echo", func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return tool.CallToolSuccess("wrapped"), nil
	})
	result, err := handler(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("wrapToolHandler() error = %v", err)
	}
	if got := mustToolText(t, result); got != "wrapped" {
		t.Fatalf("wrapToolHandler() text = %q, want wrapped", got)
	}

	staticContents, err := srv.createResourceHandler("plain://hello")(context.Background(), mcp.ReadResourceRequest{})
	if err != nil {
		t.Fatalf("createResourceHandler() error = %v", err)
	}
	if len(staticContents) != 1 {
		t.Fatalf("createResourceHandler() len = %d, want 1", len(staticContents))
	}

	templateContents, err := srv.createResourceTemplateHandler()(context.Background(), mcp.ReadResourceRequest{
		Params: mcp.ReadResourceParams{URI: "templ://world"},
	})
	if err != nil {
		t.Fatalf("createResourceTemplateHandler() error = %v", err)
	}
	if len(templateContents) != 1 {
		t.Fatalf("createResourceTemplateHandler() len = %d, want 1", len(templateContents))
	}

	httpHandler := srv.buildHTTPHandler(map[string]http.Handler{
		"/custom": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte("custom"))
		}),
	})

	rec := httptest.NewRecorder()
	httpHandler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("/health = (%d, %q), want (200, ok)", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	httpHandler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ready" {
		t.Fatalf("/ready = (%d, %q), want (200, ready)", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	httpHandler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/custom", nil))
	if rec.Code != http.StatusAccepted || rec.Body.String() != "custom" {
		t.Fatalf("/custom = (%d, %q), want (202, custom)", rec.Code, rec.Body.String())
	}
}

func TestOperationHelpersCoverage(t *testing.T) {
	t.Parallel()

	req, err := decodeOperationRequest(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"args":null}`)))
	if err != nil {
		t.Fatalf("decodeOperationRequest() error = %v", err)
	}
	if req.Args == nil || len(req.Args) != 0 {
		t.Fatalf("decodeOperationRequest() args = %#v, want empty map", req.Args)
	}

	if _, err := decodeOperationRequest(httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{"))); err == nil {
		t.Fatal("decodeOperationRequest(invalid) error = nil, want error")
	}

	if value, err := requiredOneOfStringArg(map[string]any{"cluster": "xatu"}, "datasource", "cluster"); err != nil || value != "xatu" {
		t.Fatalf("requiredOneOfStringArg() = (%q, %v), want (xatu, nil)", value, err)
	}

	if _, err := requiredOneOfStringArg(map[string]any{}, "datasource", "cluster"); err == nil {
		t.Fatal("requiredOneOfStringArg(missing) error = nil, want error")
	}

	if got := optionalMapArg(map[string]any{}, "missing"); len(got) != 0 {
		t.Fatalf("optionalMapArg(missing) = %#v, want empty map", got)
	}

	if got := optionalIntArg(map[string]any{"value": int64(9)}, "value", 1); got != 9 {
		t.Fatalf("optionalIntArg(int64) = %d, want 9", got)
	}
	if got := optionalIntArg(map[string]any{}, "value", 7); got != 7 {
		t.Fatalf("optionalIntArg(default) = %d, want 7", got)
	}

	if got, err := parseDurationSeconds(""); err != nil || got != 0 {
		t.Fatalf("parseDurationSeconds(\"\") = (%d, %v), want (0, nil)", got, err)
	}
	if _, err := parseDurationSeconds("3q"); err == nil {
		t.Fatal("parseDurationSeconds(unknown unit) error = nil, want error")
	}
	if _, err := parseDurationSeconds("xs"); err == nil {
		t.Fatal("parseDurationSeconds(invalid value) error = nil, want error")
	}

	now := time.Unix(1_700_000_000, 0).UTC()
	if got, err := parsePrometheusTime("now-1m", now); err != nil || got != "1699999940" {
		t.Fatalf("parsePrometheusTime(now-1m) = (%q, %v), want (1699999940, nil)", got, err)
	}
	if got, err := parsePrometheusTime("1700000000.9", now); err != nil || got != "1700000000" {
		t.Fatalf("parsePrometheusTime(float) = (%q, %v), want (1700000000, nil)", got, err)
	}

	if got, err := parseLokiTime("1700000000", now); err != nil || got != "1700000000000000000" {
		t.Fatalf("parseLokiTime(seconds) = (%q, %v), want seconds->nanos", got, err)
	}
	if got, err := parseLokiTime("1700000000.5", now); err != nil || got != "1700000000500000000" {
		t.Fatalf("parseLokiTime(float) = (%q, %v), want float->nanos", got, err)
	}

	rec := httptest.NewRecorder()
	writePassthroughResponse(rec, http.StatusAccepted, "text/plain", nil)
	if rec.Code != http.StatusAccepted || rec.Header().Get("X-Operation-Transport") != "passthrough" {
		t.Fatalf("writePassthroughResponse() = (%d, %#v), want passthrough accepted", rec.Code, rec.Header())
	}
}

func TestOperationHandlersCoverage(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/clickhouse/":
			if r.Header.Get("X-Datasource") != "xatu" {
				t.Fatalf("clickhouse datasource header = %q, want xatu", r.Header.Get("X-Datasource"))
			}
			if got := r.URL.Query().Get("param_enabled"); got != "" && got != "1" {
				t.Fatalf("clickhouse param_enabled = %q, want empty or 1", got)
			}
			if got := r.URL.Query().Get("param_limit"); got != "" && got != "10" {
				t.Fatalf("clickhouse param_limit = %q, want empty or 10", got)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("ReadAll(clickhouse body) error = %v", err)
			}
			if string(body) != "SELECT 1" {
				t.Fatalf("clickhouse body = %q, want SELECT 1", string(body))
			}
			w.Header().Set("Content-Type", "text/tab-separated-values")
			_, _ = w.Write([]byte("a\tb\n1\t2\n"))
		case "/prometheus/api/v1/label/job/values":
			if r.Header.Get("X-Datasource") != "metrics" {
				t.Fatalf("prometheus datasource header = %q, want metrics", r.Header.Get("X-Datasource"))
			}
			_, _ = w.Write([]byte(`{"status":"success","data":["api"]}`))
		case "/loki/loki/api/v1/labels":
			if r.Header.Get("X-Datasource") != "logs" {
				t.Fatalf("loki datasource header = %q, want logs", r.Header.Get("X-Datasource"))
			}
			_, _ = w.Write([]byte(`{"status":"success","data":["job"]}`))
		default:
			t.Fatalf("unexpected upstream path %q", r.URL.Path)
		}
	}))
	defer upstream.Close()

	srv := &service{
		log:        logrus.New(),
		httpClient: upstream.Client(),
		proxyService: &coverageProxyService{
			url: upstream.URL,
			clickhouseInfo: []types.DatasourceInfo{{
				Name:        "xatu",
				Description: "ClickHouse",
				Metadata:    map[string]string{"database": "default"},
			}},
			prometheusInfo: []types.DatasourceInfo{{
				Name:        "metrics",
				Description: "Prometheus",
				Metadata:    map[string]string{"url": "https://prom.example"},
			}},
			lokiInfo: []types.DatasourceInfo{{
				Name:        "logs",
				Description: "Loki",
				Metadata:    map[string]string{"url": "https://loki.example"},
			}},
		},
		operationHandlers: make(map[string]operationHandler, 32),
	}
	srv.registerOperations()

	rec := performOperationRequest(t, srv, "clickhouse.list_datasources", operations.Request{})
	if rec.Code != http.StatusOK {
		t.Fatalf("clickhouse.list_datasources status = %d, want 200", rec.Code)
	}
	assertDatasourceResponse(t, rec.Body.Bytes(), "xatu", "default")

	rec = performOperationRequest(t, srv, "prometheus.list_datasources", operations.Request{})
	if rec.Code != http.StatusOK {
		t.Fatalf("prometheus.list_datasources status = %d, want 200", rec.Code)
	}
	assertDatasourceResponse(t, rec.Body.Bytes(), "metrics", "")

	rec = performOperationRequest(t, srv, "loki.list_datasources", operations.Request{})
	if rec.Code != http.StatusOK {
		t.Fatalf("loki.list_datasources status = %d, want 200", rec.Code)
	}
	assertDatasourceResponse(t, rec.Body.Bytes(), "logs", "")

	rec = performOperationRequest(
		t,
		srv,
		"clickhouse.query",
		operations.TypedRequest[operations.ClickHouseQueryArgs]{Args: operations.ClickHouseQueryArgs{
			Cluster: "xatu",
			SQL:     "SELECT 1",
		}},
	)
	if rec.Code != http.StatusOK || rec.Header().Get("X-Operation-Transport") != "passthrough" {
		t.Fatalf("clickhouse.query = (%d, %#v), want passthrough success", rec.Code, rec.Header())
	}

	rec = performOperationRequest(
		t,
		srv,
		"clickhouse.query_raw",
		operations.TypedRequest[operations.ClickHouseQueryArgs]{Args: operations.ClickHouseQueryArgs{
			Datasource: "xatu",
			SQL:        "SELECT 1",
			Parameters: map[string]any{"enabled": true, "limit": 10},
		}},
	)
	if rec.Code != http.StatusOK || rec.Header().Get("X-Operation-Transport") != "passthrough" {
		t.Fatalf("clickhouse.query_raw = (%d, %#v), want passthrough success", rec.Code, rec.Header())
	}

	rec = performOperationRequest(
		t,
		srv,
		"prometheus.get_label_values",
		operations.TypedRequest[operations.DatasourceLabelArgs]{Args: operations.DatasourceLabelArgs{
			Datasource: "metrics",
			Label:      "job",
		}},
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("prometheus.get_label_values status = %d, want 200", rec.Code)
	}

	rec = performOperationRequest(
		t,
		srv,
		"loki.get_labels",
		operations.TypedRequest[operations.LokiLabelsArgs]{Args: operations.LokiLabelsArgs{
			Datasource: "logs",
		}},
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("loki.get_labels status = %d, want 200", rec.Code)
	}

	if got := formatClickHouseParamValue(true); got != "1" {
		t.Fatalf("formatClickHouseParamValue(true) = %q, want 1", got)
	}
	if got := formatClickHouseParamValue(false); got != "0" {
		t.Fatalf("formatClickHouseParamValue(false) = %q, want 0", got)
	}
	if got := formatClickHouseParamValue(nil); got != "" {
		t.Fatalf("formatClickHouseParamValue(nil) = %q, want empty string", got)
	}

	customCalled := false
	srv.registerOperation("custom", func(w http.ResponseWriter, _ *http.Request) {
		customCalled = true
		w.WriteHeader(http.StatusNoContent)
	})
	rec = httptest.NewRecorder()
	if !srv.dispatchOperation("custom", rec, httptest.NewRequest(http.MethodGet, "/", nil)) || !customCalled {
		t.Fatal("dispatchOperation(custom) failed, want custom handler")
	}
	if srv.dispatchOperation("missing", httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil)) {
		t.Fatal("dispatchOperation(missing) = true, want false")
	}
}

func assertDatasourceResponse(t *testing.T, body []byte, name, database string) {
	t.Helper()

	var response operations.Response
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("json.Unmarshal(response) error = %v", err)
	}

	payload, err := operations.DecodeResponseData[operations.DatasourcesPayload](&response)
	if err != nil {
		t.Fatalf("DecodeResponseData() error = %v", err)
	}

	if len(payload.Datasources) != 1 || payload.Datasources[0].Name != name {
		t.Fatalf("datasources payload = %#v, want single datasource %q", payload, name)
	}

	if database != "" && payload.Datasources[0].Database != database {
		t.Fatalf("datasource database = %q, want %q", payload.Datasources[0].Database, database)
	}
}

func mustToolText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()

	if len(result.Content) != 1 {
		t.Fatalf("tool content len = %d, want 1", len(result.Content))
	}

	text, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("tool content = %#v, want text", result.Content[0])
	}

	return text.Text
}

func newInitializedModuleRegistry(t *testing.T) *module.Registry {
	t.Helper()

	reg := module.NewRegistry(logrus.New())

	return reg
}
