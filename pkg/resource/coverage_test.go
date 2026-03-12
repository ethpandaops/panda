package resource

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/ethpandaops/cartographoor/pkg/discovery"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/cartographoor"
	"github.com/ethpandaops/panda/pkg/embedding"
	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/serverapi"
	"github.com/ethpandaops/panda/pkg/types"
)

type testModule struct {
	name     string
	examples map[string]types.ExampleCategory
	docs     map[string]types.ModuleDoc
	snippet  string
}

func (m *testModule) Name() string        { return m.name }
func (m *testModule) Init(_ []byte) error { return nil }
func (m *testModule) Validate() error     { return nil }
func (m *testModule) Examples() map[string]types.ExampleCategory {
	return m.examples
}
func (m *testModule) PythonAPIDocs() map[string]types.ModuleDoc {
	return m.docs
}
func (m *testModule) GettingStartedSnippet() string {
	return m.snippet
}

type testProxyService struct {
	infos []types.DatasourceInfo
}

func (s *testProxyService) Start(_ context.Context) error          { return nil }
func (s *testProxyService) Stop(_ context.Context) error           { return nil }
func (s *testProxyService) URL() string                            { return "http://proxy" }
func (s *testProxyService) AuthorizeRequest(_ *http.Request) error { return nil }
func (s *testProxyService) RegisterToken(_ string) string          { return "" }
func (s *testProxyService) RevokeToken(_ string)                   {}
func (s *testProxyService) ClickHouseDatasources() []string        { return nil }
func (s *testProxyService) ClickHouseDatasourceInfo() []types.DatasourceInfo {
	return nil
}
func (s *testProxyService) PrometheusDatasources() []string { return nil }
func (s *testProxyService) PrometheusDatasourceInfo() []types.DatasourceInfo {
	return nil
}
func (s *testProxyService) LokiDatasources() []string                  { return nil }
func (s *testProxyService) LokiDatasourceInfo() []types.DatasourceInfo { return nil }
func (s *testProxyService) S3Bucket() string                           { return "" }
func (s *testProxyService) S3PublicURLPrefix() string                  { return "" }
func (s *testProxyService) EthNodeAvailable() bool                     { return false }
func (s *testProxyService) DatasourceInfo() []types.DatasourceInfo     { return s.infos }
func (s *testProxyService) Datasources() serverapi.DatasourcesResponse {
	return serverapi.DatasourcesResponse{Datasources: s.infos}
}

type testToolLister struct {
	tools []mcp.Tool
}

func (l testToolLister) List() []mcp.Tool {
	return append([]mcp.Tool(nil), l.tools...)
}

type testCartographoorClient struct {
	all      map[string]discovery.Network
	active   map[string]discovery.Network
	groups   map[string]map[string]discovery.Network
	clusters map[string][]string
}

func (c *testCartographoorClient) Start(_ context.Context) error { return nil }
func (c *testCartographoorClient) Stop() error                   { return nil }

func (c *testCartographoorClient) GetAllNetworks() map[string]discovery.Network {
	return cloneNetworks(c.all)
}

func (c *testCartographoorClient) GetActiveNetworks() map[string]discovery.Network {
	return cloneNetworks(c.active)
}

func (c *testCartographoorClient) GetNetwork(name string) (discovery.Network, bool) {
	network, ok := c.all[name]
	return network, ok
}

func (c *testCartographoorClient) GetGroup(name string) (map[string]discovery.Network, bool) {
	networks, ok := c.groups[name]
	if !ok {
		return nil, false
	}

	return cloneNetworks(networks), true
}

func (c *testCartographoorClient) GetGroups() []string {
	groups := make([]string, 0, len(c.groups))
	for name := range c.groups {
		groups = append(groups, name)
	}

	return groups
}

func (c *testCartographoorClient) IsDevnet(network discovery.Network) bool {
	return strings.Contains(network.Repository, "devnet")
}

func (c *testCartographoorClient) GetClusters(network discovery.Network) []string {
	return append([]string(nil), c.clusters[network.Name]...)
}

var _ cartographoor.CartographoorClient = (*testCartographoorClient)(nil)

func TestRegisterAPIResources(t *testing.T) {
	t.Parallel()

	moduleReg := newInitializedModuleRegistry(t, &testModule{
		name: "docs",
		docs: map[string]types.ModuleDoc{
			"clickhouse": {
				Description: "Query ClickHouse",
				Functions: map[string]types.FunctionDoc{
					"query": {Signature: "query(sql: str) -> DataFrame"},
				},
			},
		},
	})

	reg := NewRegistry(logrus.New())
	RegisterAPIResources(logrus.New(), reg, moduleReg)

	if got := reg.ListStatic(); len(got) != 1 || got[0].URI != "python://ethpandaops" {
		t.Fatalf("ListStatic() = %#v, want python resource", got)
	}

	content, mimeType, err := reg.Read(context.Background(), "python://ethpandaops")
	if err != nil {
		t.Fatalf("Read(python://ethpandaops) error = %v", err)
	}

	if mimeType != "application/json" {
		t.Fatalf("mimeType = %q, want application/json", mimeType)
	}

	var response serverapi.APIDocResponse
	decodeJSON(t, content, &response)

	if response.Library != "ethpandaops" {
		t.Fatalf("Library = %q, want ethpandaops", response.Library)
	}

	if response.Modules["clickhouse"].Description != "Query ClickHouse" {
		t.Fatalf("clickhouse docs = %#v, want module docs to round-trip", response.Modules["clickhouse"])
	}

	storageDoc, ok := response.Modules["storage"]
	if !ok {
		t.Fatalf("storage module missing from %#v", response.Modules)
	}

	if storageDoc.Functions["upload"].Signature == "" || storageDoc.Functions["get_url"].Returns == "" {
		t.Fatalf("storage docs incomplete: %#v", storageDoc)
	}
}

func TestRegisterDatasourcesResources(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(logrus.New())
	RegisterDatasourcesResources(logrus.New(), reg, &testProxyService{
		infos: []types.DatasourceInfo{
			{Type: "clickhouse", Name: "xatu"},
			{Type: "prometheus", Name: "metrics"},
			{Type: "loki", Name: "logs"},
			{Type: "clickhouse", Name: "archive"},
		},
	})

	var all DatasourcesJSONResponse
	readJSONResource(t, reg, "datasources://list", &all)
	if len(all.Datasources) != 4 {
		t.Fatalf("datasources://list count = %d, want 4", len(all.Datasources))
	}

	var clickhouse DatasourcesJSONResponse
	readJSONResource(t, reg, "datasources://clickhouse", &clickhouse)
	if got := datasourceNames(clickhouse.Datasources); strings.Join(got, ",") != "xatu,archive" {
		t.Fatalf("clickhouse datasource names = %v, want [xatu archive]", got)
	}

	var prometheus DatasourcesJSONResponse
	readJSONResource(t, reg, "datasources://prometheus", &prometheus)
	if got := datasourceNames(prometheus.Datasources); len(got) != 1 || got[0] != "metrics" {
		t.Fatalf("prometheus datasource names = %v, want [metrics]", got)
	}

	emptyReg := NewRegistry(logrus.New())
	RegisterDatasourcesResources(logrus.New(), emptyReg, nil)

	var empty DatasourcesJSONResponse
	readJSONResource(t, emptyReg, "datasources://list", &empty)
	if empty.Datasources == nil || len(empty.Datasources) != 0 {
		t.Fatalf("empty datasources = %#v, want non-nil empty slice", empty.Datasources)
	}
}

func TestRegisterExamplesResources(t *testing.T) {
	t.Parallel()

	moduleReg := newInitializedModuleRegistry(t, &testModule{
		name: "examples",
		examples: map[string]types.ExampleCategory{
			"queries": {
				Name:        "Queries",
				Description: "Useful starter queries",
				Examples: []types.Example{
					{Name: "Head blocks", Query: "SELECT 1"},
				},
			},
		},
	})
	reg := NewRegistry(logrus.New())

	RegisterExamplesResources(logrus.New(), reg, moduleReg)

	var examples map[string]types.ExampleCategory
	readJSONResource(t, reg, "examples://queries", &examples)

	if got := examples["queries"].Examples[0].Name; got != "Head blocks" {
		t.Fatalf("examples://queries first example = %q, want Head blocks", got)
	}

	gotExamples := GetQueryExamples(moduleReg)
	if gotExamples["queries"].Name != "Queries" {
		t.Fatalf("GetQueryExamples() = %#v, want module registry examples", gotExamples)
	}
}

func TestRegisterGettingStartedResources(t *testing.T) {
	t.Parallel()

	log := logrus.New()
	reg := NewRegistry(log)
	moduleReg := newInitializedModuleRegistry(t, &testModule{
		name:    "snippet",
		snippet: "### Custom setup\n\nRead this first.\n",
	})

	reg.RegisterStatic(StaticResource{
		Resource: mcp.NewResource("examples://queries", "Examples", mcp.WithMIMEType("application/json")),
		Handler:  func(_ context.Context, _ string) (string, error) { return "{}", nil },
	})
	reg.RegisterStatic(StaticResource{
		Resource: mcp.NewResource("python://ethpandaops", "API", mcp.WithMIMEType("application/json")),
		Handler:  func(_ context.Context, _ string) (string, error) { return "{}", nil },
	})
	reg.RegisterTemplate(TemplateResource{
		Template: mcp.NewResourceTemplate(
			"networks://{name}",
			"Network Details",
			mcp.WithTemplateMIMEType("application/json"),
		),
		Pattern: networkURIPattern,
		Handler: func(_ context.Context, _ string) (string, error) { return "{}", nil },
	})

	RegisterGettingStartedResources(log, reg, testToolLister{
		tools: []mcp.Tool{
			mcp.NewTool("search", mcp.WithDescription("Search docs\nMore detail")),
			mcp.NewTool("execute_python", mcp.WithDescription("Run Python snippets")),
		},
	}, moduleReg)

	content, mimeType, err := reg.Read(context.Background(), "ethpandaops://getting-started")
	if err != nil {
		t.Fatalf("Read(ethpandaops://getting-started) error = %v", err)
	}

	if mimeType != "text/markdown" {
		t.Fatalf("mimeType = %q, want text/markdown", mimeType)
	}

	required := []string{
		"# Getting Started Guide",
		"### Custom setup",
		"## Available Tools",
		"- **execute_python**: Run Python snippets",
		"- **search**: Search docs",
		"## Available Resources",
		"- `examples://queries` - Examples",
		"- `python://ethpandaops` - API",
		"**Templates:**",
		"- `networks://{name}` - Network Details",
		"## Sessions",
	}

	for _, needle := range required {
		if !strings.Contains(content, needle) {
			t.Fatalf("getting-started content missing %q\n%s", needle, content)
		}
	}

	if strings.Contains(content, "ethpandaops://getting-started` - Getting Started Guide") {
		t.Fatalf("getting-started content should skip self-reference\n%s", content)
	}

	if strings.Index(content, "- **execute_python**") > strings.Index(content, "- **search**") {
		t.Fatalf("tools not sorted alphabetically\n%s", content)
	}

	if strings.Index(content, "- `examples://queries`") > strings.Index(content, "- `python://ethpandaops`") {
		t.Fatalf("resources not sorted alphabetically\n%s", content)
	}
}

func TestRegisterNetworksResources(t *testing.T) {
	t.Parallel()

	log := logrus.New()
	reg := NewRegistry(log)
	client := &testCartographoorClient{
		all: map[string]discovery.Network{
			"mainnet": {
				Name:       "mainnet",
				ChainID:    1,
				Status:     "active",
				Repository: "ethpandaops/mainnet",
			},
			"fusaka-devnet-1": {
				Name:       "fusaka-devnet-1",
				ChainID:    701,
				Status:     "active",
				Repository: "ethpandaops/fusaka-devnets",
			},
		},
		active: map[string]discovery.Network{
			"mainnet": {
				Name:       "mainnet",
				ChainID:    1,
				Status:     "active",
				Repository: "ethpandaops/mainnet",
			},
		},
		groups: map[string]map[string]discovery.Network{
			"fusaka": {
				"fusaka-devnet-1": {
					Name:       "fusaka-devnet-1",
					ChainID:    701,
					Status:     "active",
					Repository: "ethpandaops/fusaka-devnets",
				},
			},
		},
		clusters: map[string][]string{
			"mainnet":         {"xatu", "xatu-cbt"},
			"fusaka-devnet-1": {"xatu-experimental", "xatu-cbt"},
		},
	}

	RegisterNetworksResources(log, reg, client)

	var active NetworksActiveResponse
	readJSONResource(t, reg, "networks://active", &active)
	if len(active.Networks) != 1 || active.Networks[0].Name != "mainnet" {
		t.Fatalf("active networks = %#v, want mainnet summary", active.Networks)
	}

	var all NetworksAllResponse
	readJSONResource(t, reg, "networks://all", &all)
	if got := all.Networks["fusaka-devnet-1"].Clusters; strings.Join(got, ",") != "xatu-experimental,xatu-cbt" {
		t.Fatalf("all networks clusters = %v, want devnet clusters", got)
	}

	var networkDetail NetworkDetailResponse
	readJSONResource(t, reg, "networks://mainnet", &networkDetail)
	if networkDetail.Network.Name != "mainnet" || networkDetail.Network.ChainID != 1 {
		t.Fatalf("network detail = %#v, want mainnet detail", networkDetail.Network)
	}

	var groupDetail GroupDetailResponse
	readJSONResource(t, reg, "networks://fusaka", &groupDetail)
	if groupDetail.Group != "fusaka" || len(groupDetail.Networks) != 1 {
		t.Fatalf("group detail = %#v, want fusaka group", groupDetail)
	}

	handler := createNetworkDetailHandler(log, client)
	if _, err := handler(context.Background(), "bad-uri"); err == nil || !strings.Contains(err.Error(), "invalid URI format") {
		t.Fatalf("invalid URI error = %v, want invalid URI format", err)
	}

	if _, _, err := reg.Read(context.Background(), "networks://unknown"); err == nil || !strings.Contains(err.Error(), `network or group "unknown" not found`) {
		t.Fatalf("unknown network error = %v, want helpful not-found message", err)
	}
}

func TestExampleIndexHelpers(t *testing.T) {
	t.Parallel()

	if got := dotProduct([]float32{1, 2, 3}, []float32{4, 5, 6}); got != 32 {
		t.Fatalf("dotProduct() = %v, want 32", got)
	}

	idx := &ExampleIndex{
		embedder: &embedding.Embedder{},
		examples: []indexedExample{{CategoryKey: "queries"}},
	}

	if err := idx.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if idx.embedder != nil || idx.examples != nil {
		t.Fatalf("Close() did not clear index state: %#v", idx)
	}
}

func TestRegistryReadAndList(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(logrus.New())
	reg.RegisterStatic(StaticResource{
		Resource: mcp.NewResource("examples://queries", "Examples", mcp.WithMIMEType("application/json")),
		Handler: func(_ context.Context, _ string) (string, error) {
			return `{"ok":true}`, nil
		},
	})
	reg.RegisterTemplate(TemplateResource{
		Template: mcp.NewResourceTemplate(
			"networks://{name}",
			"Network Details",
			mcp.WithTemplateMIMEType("application/json"),
		),
		Pattern: networkURIPattern,
		Handler: func(_ context.Context, uri string) (string, error) {
			return `{"uri":"` + uri + `"}`, nil
		},
	})

	if got := reg.ListStatic(); len(got) != 1 || got[0].Name != "Examples" {
		t.Fatalf("ListStatic() = %#v, want registered static resource", got)
	}

	if got := reg.ListTemplates(); len(got) != 1 || got[0].Name != "Network Details" {
		t.Fatalf("ListTemplates() = %#v, want registered template", got)
	}

	content, mimeType, err := reg.Read(context.Background(), "examples://queries")
	if err != nil {
		t.Fatalf("Read(static) error = %v", err)
	}

	if mimeType != "application/json" || content != `{"ok":true}` {
		t.Fatalf("Read(static) = (%q, %q), want json payload", content, mimeType)
	}

	content, mimeType, err = reg.Read(context.Background(), "networks://mainnet")
	if err != nil {
		t.Fatalf("Read(template) error = %v", err)
	}

	if mimeType != "application/json" || !strings.Contains(content, `"uri":"networks://mainnet"`) {
		t.Fatalf("Read(template) = (%q, %q), want template handler payload", content, mimeType)
	}
}

func TestRegistryReadErrors(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(logrus.New())
	reg.RegisterStatic(StaticResource{
		Resource: mcp.NewResource("examples://broken", "Broken", mcp.WithMIMEType("text/plain")),
		Handler: func(_ context.Context, _ string) (string, error) {
			return "", context.DeadlineExceeded
		},
	})
	reg.RegisterTemplate(TemplateResource{
		Template: mcp.NewResourceTemplate("networks://{name}", "Broken Template"),
		Pattern:  networkURIPattern,
		Handler: func(_ context.Context, _ string) (string, error) {
			return "", context.Canceled
		},
	})

	if _, _, err := reg.Read(context.Background(), "examples://broken"); err == nil || !strings.Contains(err.Error(), "reading static resource examples://broken") {
		t.Fatalf("static error = %v, want wrapped static handler error", err)
	}

	if _, _, err := reg.Read(context.Background(), "networks://broken"); err == nil || !strings.Contains(err.Error(), "reading template resource networks://broken") {
		t.Fatalf("template error = %v, want wrapped template handler error", err)
	}

	if _, _, err := reg.Read(context.Background(), "unknown://resource"); err == nil || !strings.Contains(err.Error(), "unknown resource URI") {
		t.Fatalf("unknown resource error = %v, want unknown resource URI", err)
	}
}

func TestRunbookIndexHelpers(t *testing.T) {
	t.Parallel()

	runbook := types.Runbook{
		Name:        "Investigate Finality Delay",
		Description: "Check why finality is delayed",
		Tags:        []string{"finality", "consensus"},
		Content:     "Start with cluster health.\nContinue reading.\n## Deep dive\nIgnored section.",
	}

	searchText := buildRunbookSearchText(runbook)
	if !strings.Contains(searchText, "Investigate Finality Delay") || !strings.Contains(searchText, "finality consensus") {
		t.Fatalf("buildRunbookSearchText() = %q, want runbook name and tags", searchText)
	}

	overview := extractOverview("Line one\nLine two\n```sql\nSELECT 1\n```", 300)
	if overview != "Line one Line two" {
		t.Fatalf("extractOverview() = %q, want content before code block", overview)
	}

	truncated := extractOverview("a very long first line that should stop once the builder exceeds the limit", 10)
	if truncated == "" || len(truncated) <= 10 {
		t.Fatalf("extractOverview() = %q, want non-empty truncated overview", truncated)
	}

	idx := &RunbookIndex{
		embedder: &embedding.Embedder{},
		runbooks: []indexedRunbook{{Runbook: runbook}},
	}

	if err := idx.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if idx.embedder != nil || idx.runbooks != nil {
		t.Fatalf("Close() did not clear runbook index state: %#v", idx)
	}
}

func newInitializedModuleRegistry(t *testing.T, modules ...module.Module) *module.Registry {
	t.Helper()

	reg := module.NewRegistry(logrus.New())

	for _, mod := range modules {
		reg.Add(mod)
		if err := reg.InitModule(mod.Name(), nil); err != nil {
			t.Fatalf("InitModule(%q) error = %v", mod.Name(), err)
		}
	}

	return reg
}

func readJSONResource(t *testing.T, reg Registry, uri string, out any) {
	t.Helper()

	content, mimeType, err := reg.Read(context.Background(), uri)
	if err != nil {
		t.Fatalf("Read(%q) error = %v", uri, err)
	}

	if mimeType != "application/json" {
		t.Fatalf("Read(%q) mimeType = %q, want application/json", uri, mimeType)
	}

	decodeJSON(t, content, out)
}

func decodeJSON(t *testing.T, data string, out any) {
	t.Helper()

	if err := json.Unmarshal([]byte(data), out); err != nil {
		t.Fatalf("json.Unmarshal(%q) error = %v", data, err)
	}
}

func datasourceNames(infos []types.DatasourceInfo) []string {
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		names = append(names, info.Name)
	}

	return names
}

func cloneNetworks(in map[string]discovery.Network) map[string]discovery.Network {
	if in == nil {
		return nil
	}

	out := make(map[string]discovery.Network, len(in))
	for name, network := range in {
		out[name] = network
	}

	return out
}
