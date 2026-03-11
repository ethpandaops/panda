package app

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/ethpandaops/cartographoor/pkg/discovery"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/cartographoor"
	"github.com/ethpandaops/panda/pkg/config"
	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/proxy"
	"github.com/ethpandaops/panda/pkg/sandbox"
	"github.com/ethpandaops/panda/pkg/types"
)

func TestBuildVariantsUseSharedPipeline(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	cases := []struct {
		name        string
		run         func(*App, context.Context) error
		wantCalls   []string
		wantSandbox bool
		wantCart    bool
	}{
		{
			name: "build-light",
			run: func(app *App, ctx context.Context) error {
				return app.BuildLight(ctx)
			},
			wantCalls: []string{
				"registry-build",
				"proxy-build",
				"proxy-start",
				"proxy-injected",
				"module-start",
			},
		},
		{
			name: "build-with-sandbox",
			run: func(app *App, ctx context.Context) error {
				return app.BuildWithSandbox(ctx)
			},
			wantCalls: []string{
				"registry-build",
				"sandbox-build",
				"sandbox-start",
				"proxy-build",
				"proxy-start",
				"proxy-injected",
				"module-start",
			},
			wantSandbox: true,
		},
		{
			name: "build-full",
			run: func(app *App, ctx context.Context) error {
				return app.Build(ctx)
			},
			wantCalls: []string{
				"registry-build",
				"sandbox-build",
				"sandbox-start",
				"proxy-build",
				"proxy-start",
				"proxy-injected",
				"module-start",
				"cart-build",
				"cart-start",
				"cart-injected",
			},
			wantSandbox: true,
			wantCart:    true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app, moduleStub, sandboxStub, cartStub, calls := newTestApp(t)

			if err := tc.run(app, ctx); err != nil {
				t.Fatalf("build failed: %v", err)
			}

			if !slices.Equal(*calls, tc.wantCalls) {
				t.Fatalf("unexpected call order: got %v want %v", *calls, tc.wantCalls)
			}

			if got := sandboxStub.startCalls > 0; got != tc.wantSandbox {
				t.Fatalf("sandbox started = %v, want %v", got, tc.wantSandbox)
			}

			if got := cartStub.startCalls > 0; got != tc.wantCart {
				t.Fatalf("cartographoor started = %v, want %v", got, tc.wantCart)
			}

			if moduleStub.proxyClient == nil {
				t.Fatal("proxy client was not injected before module start")
			}

			if tc.wantCart && moduleStub.cartClient == nil {
				t.Fatal("cartographoor client was not injected for full build")
			}

			if !tc.wantCart && moduleStub.cartClient != nil {
				t.Fatal("cartographoor client should not be injected for this build variant")
			}
		})
	}
}

func TestBuildStopsStartedServicesWhenCartographoorStartFails(t *testing.T) {
	t.Parallel()

	app, moduleStub, sandboxStub, cartStub, calls := newTestApp(t)
	cartStub.startErr = errors.New("cart failed")

	err := app.Build(context.Background())
	if err == nil {
		t.Fatal("expected build to fail")
	}

	if got := err.Error(); got != "starting cartographoor client: cart failed" {
		t.Fatalf("unexpected error: %s", got)
	}

	if sandboxStub.stopCalls != 1 {
		t.Fatalf("sandbox stop calls = %d, want 1", sandboxStub.stopCalls)
	}

	if app.ProxyClient.(*fakeProxyClient).stopCalls != 1 {
		t.Fatalf("proxy stop calls = %d, want 1", app.ProxyClient.(*fakeProxyClient).stopCalls)
	}

	if moduleStub.stopCalls != 1 {
		t.Fatalf("module stop calls = %d, want 1", moduleStub.stopCalls)
	}

	if cartStub.stopCalls != 0 {
		t.Fatalf("cartographoor stop calls = %d, want 0", cartStub.stopCalls)
	}

	if !slices.Contains(*calls, "module-stop") || !slices.Contains(*calls, "sandbox-stop") {
		t.Fatalf("expected cleanup calls, got %v", *calls)
	}
}

func newTestApp(t *testing.T) (*App, *fakeModule, *fakeSandboxService, *fakeCartographoorClient, *[]string) {
	t.Helper()

	calls := &[]string{}
	logger := logrus.New()
	moduleStub := &fakeModule{name: "fake", calls: calls}
	registry := newInitializedRegistry(t, logger, moduleStub)
	sandboxStub := &fakeSandboxService{calls: calls}
	proxyStub := &fakeProxyClient{url: "http://proxy", calls: calls}
	cartStub := &fakeCartographoorClient{calls: calls}

	app := New(logger, &config.Config{
		Proxy: config.ProxyConfig{URL: proxyStub.url},
	})
	app.moduleRegistryBuilder = func() (*module.Registry, error) {
		*calls = append(*calls, "registry-build")
		return registry, nil
	}
	app.sandboxBuilder = func() (sandbox.Service, error) {
		*calls = append(*calls, "sandbox-build")
		return sandboxStub, nil
	}
	app.proxyClientBuilder = func() proxy.Client {
		*calls = append(*calls, "proxy-build")
		return proxyStub
	}
	app.cartographoorBuilder = func() cartographoor.CartographoorClient {
		*calls = append(*calls, "cart-build")
		return cartStub
	}

	return app, moduleStub, sandboxStub, cartStub, calls
}

func newInitializedRegistry(t *testing.T, logger logrus.FieldLogger, mod module.Module) *module.Registry {
	t.Helper()

	registry := module.NewRegistry(logger)
	registry.Add(mod)
	if err := registry.InitModule(mod.Name(), nil); err != nil {
		t.Fatalf("init module: %v", err)
	}

	return registry
}

type fakeModule struct {
	name        string
	startErr    error
	startCalls  int
	stopCalls   int
	proxyClient proxy.ClickHouseSchemaAccess
	cartClient  cartographoor.CartographoorClient
	calls       *[]string
}

func (f *fakeModule) Name() string { return f.name }

func (f *fakeModule) Init(_ []byte) error { return nil }

func (f *fakeModule) ApplyDefaults() {}

func (f *fakeModule) Validate() error { return nil }

func (f *fakeModule) Start(context.Context) error {
	f.startCalls++
	*f.calls = append(*f.calls, "module-start")
	return f.startErr
}

func (f *fakeModule) Stop(context.Context) error {
	f.stopCalls++
	*f.calls = append(*f.calls, "module-stop")
	return nil
}

func (f *fakeModule) SetProxyClient(client proxy.ClickHouseSchemaAccess) {
	f.proxyClient = client
	*f.calls = append(*f.calls, "proxy-injected")
}

func (f *fakeModule) SetCartographoorClient(client cartographoor.CartographoorClient) {
	f.cartClient = client
	*f.calls = append(*f.calls, "cart-injected")
}

type fakeSandboxService struct {
	startCalls int
	stopCalls  int
	calls      *[]string
}

func (f *fakeSandboxService) Start(context.Context) error {
	f.startCalls++
	*f.calls = append(*f.calls, "sandbox-start")
	return nil
}

func (f *fakeSandboxService) Stop(context.Context) error {
	f.stopCalls++
	*f.calls = append(*f.calls, "sandbox-stop")
	return nil
}

func (f *fakeSandboxService) Execute(context.Context, sandbox.ExecuteRequest) (*sandbox.ExecutionResult, error) {
	return nil, nil
}

func (f *fakeSandboxService) Name() string { return "fake-sandbox" }

func (f *fakeSandboxService) ListSessions(context.Context, string) ([]sandbox.SessionInfo, error) {
	return nil, nil
}

func (f *fakeSandboxService) CreateSession(context.Context, string, map[string]string) (*sandbox.CreatedSession, error) {
	return &sandbox.CreatedSession{}, nil
}

func (f *fakeSandboxService) DestroySession(context.Context, string, string) error { return nil }

func (f *fakeSandboxService) CanCreateSession(context.Context, string) (bool, int, int) {
	return true, 0, 0
}

func (f *fakeSandboxService) SessionsEnabled() bool { return false }

type fakeProxyClient struct {
	url        string
	startErr   error
	startCalls int
	stopCalls  int
	calls      *[]string
}

func (f *fakeProxyClient) Start(context.Context) error {
	f.startCalls++
	*f.calls = append(*f.calls, "proxy-start")
	return f.startErr
}

func (f *fakeProxyClient) Stop(context.Context) error {
	f.stopCalls++
	*f.calls = append(*f.calls, "proxy-stop")
	return nil
}

func (f *fakeProxyClient) URL() string { return f.url }

func (f *fakeProxyClient) AuthorizeRequest(*http.Request) error { return nil }

func (f *fakeProxyClient) RegisterToken(string) string { return "" }

func (f *fakeProxyClient) RevokeToken(string) {}

func (f *fakeProxyClient) ClickHouseDatasources() []string { return nil }

func (f *fakeProxyClient) ClickHouseDatasourceInfo() []types.DatasourceInfo { return nil }

func (f *fakeProxyClient) PrometheusDatasources() []string { return nil }

func (f *fakeProxyClient) PrometheusDatasourceInfo() []types.DatasourceInfo { return nil }

func (f *fakeProxyClient) LokiDatasources() []string { return nil }

func (f *fakeProxyClient) LokiDatasourceInfo() []types.DatasourceInfo { return nil }

func (f *fakeProxyClient) S3Bucket() string { return "" }

func (f *fakeProxyClient) S3PublicURLPrefix() string { return "" }

func (f *fakeProxyClient) EthNodeAvailable() bool { return false }

func (f *fakeProxyClient) DatasourceInfo() []types.DatasourceInfo { return nil }

func (f *fakeProxyClient) Discover(context.Context) error { return nil }

func (f *fakeProxyClient) EnsureAuthenticated(context.Context) error { return nil }

type fakeCartographoorClient struct {
	startErr   error
	startCalls int
	stopCalls  int
	calls      *[]string
}

func (f *fakeCartographoorClient) Start(context.Context) error {
	f.startCalls++
	*f.calls = append(*f.calls, "cart-start")
	return f.startErr
}

func (f *fakeCartographoorClient) Stop() error {
	f.stopCalls++
	*f.calls = append(*f.calls, "cart-stop")
	return nil
}

func (f *fakeCartographoorClient) GetAllNetworks() map[string]discovery.Network { return nil }

func (f *fakeCartographoorClient) GetActiveNetworks() map[string]discovery.Network { return nil }

func (f *fakeCartographoorClient) GetNetwork(string) (discovery.Network, bool) {
	return discovery.Network{}, false
}

func (f *fakeCartographoorClient) GetGroup(string) (map[string]discovery.Network, bool) {
	return nil, false
}

func (f *fakeCartographoorClient) GetGroups() []string { return nil }

func (f *fakeCartographoorClient) IsDevnet(discovery.Network) bool { return false }

func (f *fakeCartographoorClient) GetClusters(discovery.Network) []string { return nil }

var (
	_ module.Module                     = (*fakeModule)(nil)
	_ module.ProxyAware                 = (*fakeModule)(nil)
	_ module.CartographoorAware         = (*fakeModule)(nil)
	_ sandbox.Service                   = (*fakeSandboxService)(nil)
	_ proxy.Client                      = (*fakeProxyClient)(nil)
	_ cartographoor.CartographoorClient = (*fakeCartographoorClient)(nil)
)
