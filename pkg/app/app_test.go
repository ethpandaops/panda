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
	"github.com/ethpandaops/panda/pkg/serverapi"
	"github.com/ethpandaops/panda/pkg/types"
)

func TestBootstrapUsesSharedPipeline(t *testing.T) {
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
			name: "minimal",
			run: func(app *App, ctx context.Context) error {
				return app.Bootstrap(ctx, BootstrapOptions{})
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
			name: "with-sandbox",
			run: func(app *App, ctx context.Context) error {
				return app.Bootstrap(ctx, BootstrapOptions{StartSandbox: true})
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
			name: "server",
			run: func(app *App, ctx context.Context) error {
				return app.Bootstrap(ctx, BootstrapOptions{
					StartSandbox:       true,
					StartCartographoor: true,
				})
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

func TestBootstrapStopsStartedServicesWhenModuleStartFails(t *testing.T) {
	t.Parallel()

	app, moduleStub, sandboxStub, _, calls := newTestApp(t)
	moduleStub.startErr = errors.New("module failed")

	err := app.Bootstrap(context.Background(), BootstrapOptions{StartSandbox: true})
	if err == nil {
		t.Fatal("expected build to fail")
	}

	if got := err.Error(); got != "starting modules: starting module \"fake\": module failed" {
		t.Fatalf("unexpected error: %s", got)
	}

	if sandboxStub.stopCalls != 1 {
		t.Fatalf("sandbox stop calls = %d, want 1", sandboxStub.stopCalls)
	}

	if app.ProxyService.(*fakeProxyClient).stopCalls != 1 {
		t.Fatalf("proxy stop calls = %d, want 1", app.ProxyService.(*fakeProxyClient).stopCalls)
	}

	if moduleStub.stopCalls != 1 {
		t.Fatalf("module stop calls = %d, want 1", moduleStub.stopCalls)
	}

	if slices.Contains(*calls, "cart-start") {
		t.Fatalf("cartographoor should not start on module failure, got calls %v", *calls)
	}
}

func TestBootstrapStopsStartedServicesWhenCartographoorStartFails(t *testing.T) {
	t.Parallel()

	app, moduleStub, sandboxStub, cartStub, calls := newTestApp(t)
	cartStub.startErr = errors.New("cart failed")

	err := app.Bootstrap(context.Background(), BootstrapOptions{
		StartSandbox:       true,
		StartCartographoor: true,
	})
	if err == nil {
		t.Fatal("expected build to fail")
	}

	if got := err.Error(); got != "starting cartographoor client: cart failed" {
		t.Fatalf("unexpected error: %s", got)
	}

	if sandboxStub.stopCalls != 1 {
		t.Fatalf("sandbox stop calls = %d, want 1", sandboxStub.stopCalls)
	}

	if app.ProxyService.(*fakeProxyClient).stopCalls != 1 {
		t.Fatalf("proxy stop calls = %d, want 1", app.ProxyService.(*fakeProxyClient).stopCalls)
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

func TestBuildSandboxEnvIncludesModuleEnvAndAPIURL(t *testing.T) {
	t.Parallel()

	app, moduleStub, _, _, _ := newTestApp(t)
	moduleStub.env = map[string]string{"MODULE_ENV": "value"}

	if err := app.Bootstrap(context.Background(), BootstrapOptions{}); err != nil {
		t.Fatalf("bootstrap failed: %v", err)
	}

	env, err := app.BuildSandboxEnv()
	if err != nil {
		t.Fatalf("BuildSandboxEnv() error = %v", err)
	}

	if got := env["MODULE_ENV"]; got != "value" {
		t.Fatalf("MODULE_ENV = %q, want %q", got, "value")
	}

	if got := env["ETHPANDAOPS_API_URL"]; got != "http://sandbox.example" {
		t.Fatalf("ETHPANDAOPS_API_URL = %q, want %q", got, "http://sandbox.example")
	}
}

func TestBootstrapReturnsModuleRegistryBuildError(t *testing.T) {
	t.Parallel()

	app := New(logrus.New(), &config.Config{})
	app.moduleRegistryBuilder = func() (*module.Registry, error) {
		return nil, errors.New("registry failed")
	}

	err := app.Bootstrap(context.Background(), BootstrapOptions{})
	if err == nil {
		t.Fatal("expected bootstrap to fail")
	}

	if got := err.Error(); got != "building module registry: registry failed" {
		t.Fatalf("unexpected error: %s", got)
	}
}

func TestStartSandboxSurfacesBuildAndStartErrors(t *testing.T) {
	t.Parallel()

	t.Run("builder error", func(t *testing.T) {
		t.Parallel()

		app, _, _, _, _ := newTestApp(t)
		app.sandboxBuilder = func() (sandbox.Service, error) {
			return nil, errors.New("sandbox builder failed")
		}

		err := app.startSandbox(context.Background())
		if err == nil {
			t.Fatal("expected startSandbox to fail")
		}

		if got := err.Error(); got != "building sandbox: sandbox builder failed" {
			t.Fatalf("unexpected error: %s", got)
		}
	})

	t.Run("start error", func(t *testing.T) {
		t.Parallel()

		app, _, sandboxStub, _, _ := newTestApp(t)
		sandboxStub.startErr = errors.New("sandbox start failed")

		err := app.startSandbox(context.Background())
		if err == nil {
			t.Fatal("expected startSandbox to fail")
		}

		if got := err.Error(); got != "starting sandbox: sandbox start failed" {
			t.Fatalf("unexpected error: %s", got)
		}

		if app.Sandbox != nil {
			t.Fatal("sandbox should not be assigned when start fails")
		}
	})
}

func TestBootstrapStopsStartedServicesWhenProxyStartFails(t *testing.T) {
	t.Parallel()

	app, moduleStub, sandboxStub, _, calls := newTestApp(t)
	proxyStub := &fakeProxyClient{
		url:      "http://proxy",
		startErr: errors.New("proxy failed"),
		calls:    calls,
	}
	app.proxyServiceBuilder = func() proxy.Service {
		*calls = append(*calls, "proxy-build")
		return proxyStub
	}

	err := app.Bootstrap(context.Background(), BootstrapOptions{StartSandbox: true})
	if err == nil {
		t.Fatal("expected bootstrap to fail")
	}

	if got := err.Error(); got != "starting proxy client: proxy failed" {
		t.Fatalf("unexpected error: %s", got)
	}

	if sandboxStub.stopCalls != 1 {
		t.Fatalf("sandbox stop calls = %d, want 1", sandboxStub.stopCalls)
	}

	if moduleStub.stopCalls != 1 {
		t.Fatalf("module stop calls = %d, want 1", moduleStub.stopCalls)
	}

	if moduleStub.startCalls != 0 {
		t.Fatalf("module start calls = %d, want 0", moduleStub.startCalls)
	}

	if proxyStub.stopCalls != 0 {
		t.Fatalf("proxy stop calls = %d, want 0", proxyStub.stopCalls)
	}

	if slices.Contains(*calls, "cart-start") {
		t.Fatalf("cartographoor should not start on proxy failure, got calls %v", *calls)
	}
}

func TestAppConfigStopAndBuildModuleRegistry(t *testing.T) {
	t.Parallel()

	app, moduleStub, sandboxStub, cartStub, _ := newTestApp(t)
	if got := app.Config(); got != app.cfg {
		t.Fatal("Config should return the app configuration pointer")
	}

	registry, err := app.buildModuleRegistry()
	if err != nil {
		t.Fatalf("buildModuleRegistry failed: %v", err)
	}

	gotModules := registry.All()
	slices.Sort(gotModules)

	if !slices.Equal(gotModules, []string{"clickhouse", "dora", "ethnode", "loki", "prometheus"}) {
		t.Fatalf("unexpected compiled module names: %v", gotModules)
	}

	if err := app.Bootstrap(context.Background(), BootstrapOptions{
		StartSandbox:       true,
		StartCartographoor: true,
	}); err != nil {
		t.Fatalf("bootstrap failed: %v", err)
	}

	if err := app.Stop(context.Background()); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	if cartStub.stopCalls != 1 {
		t.Fatalf("cartographoor stop calls = %d, want 1", cartStub.stopCalls)
	}

	if moduleStub.stopCalls != 1 {
		t.Fatalf("module stop calls = %d, want 1", moduleStub.stopCalls)
	}

	if app.ProxyService.(*fakeProxyClient).stopCalls != 1 {
		t.Fatalf("proxy stop calls = %d, want 1", app.ProxyService.(*fakeProxyClient).stopCalls)
	}

	if sandboxStub.stopCalls != 1 {
		t.Fatalf("sandbox stop calls = %d, want 1", sandboxStub.stopCalls)
	}
}

func TestBuildSandboxEnvErrorsAndSandboxAPIURLFallbacks(t *testing.T) {
	t.Parallel()

	t.Run("module registry is required", func(t *testing.T) {
		t.Parallel()

		app := New(logrus.New(), &config.Config{})

		_, err := app.BuildSandboxEnv()
		if err == nil {
			t.Fatal("expected BuildSandboxEnv to fail")
		}

		if got := err.Error(); got != "module registry is not initialized" {
			t.Fatalf("unexpected error: %s", got)
		}
	})

	t.Run("module sandbox env errors are wrapped", func(t *testing.T) {
		t.Parallel()

		app, moduleStub, _, _, _ := newTestApp(t)
		moduleStub.envErr = errors.New("env failed")

		if err := app.Bootstrap(context.Background(), BootstrapOptions{}); err != nil {
			t.Fatalf("bootstrap failed: %v", err)
		}

		_, err := app.BuildSandboxEnv()
		if err == nil {
			t.Fatal("expected BuildSandboxEnv to fail")
		}

		if got := err.Error(); got != "collecting sandbox env: getting sandbox env for module \"fake\": env failed" {
			t.Fatalf("unexpected error: %s", got)
		}
	})

	t.Run("fallback API URL uses host.docker.internal", func(t *testing.T) {
		t.Parallel()

		logger := logrus.New()
		calls := &[]string{}
		mod := &fakeModule{
			name:  "fake",
			env:   map[string]string{"MODULE_ENV": "value"},
			calls: calls,
		}
		app := New(logger, &config.Config{
			Server: config.ServerConfig{Port: 3030},
		})
		app.ModuleRegistry = newInitializedRegistry(t, logger, mod)

		env, err := app.BuildSandboxEnv()
		if err != nil {
			t.Fatalf("BuildSandboxEnv failed: %v", err)
		}

		if got := env["ETHPANDAOPS_API_URL"]; got != "http://host.docker.internal:3030" {
			t.Fatalf("ETHPANDAOPS_API_URL = %q, want %q", got, "http://host.docker.internal:3030")
		}
	})

	t.Run("sandboxAPIURL honors precedence", func(t *testing.T) {
		t.Parallel()

		var nilApp *App
		if got := nilApp.sandboxAPIURL(); got != "" {
			t.Fatalf("sandboxAPIURL(nil app) = %q, want empty string", got)
		}

		appWithNilConfig := &App{}
		if got := appWithNilConfig.sandboxAPIURL(); got != "" {
			t.Fatalf("sandboxAPIURL(nil config) = %q, want empty string", got)
		}

		cases := []struct {
			name string
			cfg  *config.Config
			want string
		}{
			{
				name: "sandbox_url",
				cfg: &config.Config{
					Server: config.ServerConfig{
						SandboxURL: " https://sandbox.example/ ",
						BaseURL:    "https://base.example/",
						URL:        "https://url.example/",
						Port:       9999,
					},
				},
				want: "https://sandbox.example",
			},
			{
				name: "base_url",
				cfg: &config.Config{
					Server: config.ServerConfig{
						BaseURL: "https://base.example/",
						URL:     "https://url.example/",
						Port:    9999,
					},
				},
				want: "https://base.example",
			},
			{
				name: "server_url",
				cfg: &config.Config{
					Server: config.ServerConfig{
						URL:  "https://url.example/",
						Port: 9999,
					},
				},
				want: "https://url.example",
			},
			{
				name: "default_port",
				cfg: &config.Config{
					Server: config.ServerConfig{},
				},
				want: "http://host.docker.internal:2480",
			},
		}

		for _, tc := range cases {
			if got := (&App{cfg: tc.cfg}).sandboxAPIURL(); got != tc.want {
				t.Fatalf("%s: sandboxAPIURL() = %q, want %q", tc.name, got, tc.want)
			}
		}
	})
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
		Proxy:  config.ProxyConfig{URL: proxyStub.url},
		Server: config.ServerConfig{SandboxURL: "http://sandbox.example/"},
	})
	app.moduleRegistryBuilder = func() (*module.Registry, error) {
		*calls = append(*calls, "registry-build")
		return registry, nil
	}
	app.sandboxBuilder = func() (sandbox.Service, error) {
		*calls = append(*calls, "sandbox-build")
		return sandboxStub, nil
	}
	app.proxyServiceBuilder = func() proxy.Service {
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
	env         map[string]string
	envErr      error
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

func (f *fakeModule) BindRuntimeDependencies(deps module.RuntimeDependencies) {
	if deps.ProxySchemaAccess != nil && f.proxyClient == nil {
		*f.calls = append(*f.calls, "proxy-injected")
	}

	if deps.Cartographoor != nil && f.cartClient == nil {
		*f.calls = append(*f.calls, "cart-injected")
	}

	f.proxyClient = deps.ProxySchemaAccess
	f.cartClient = deps.Cartographoor
}

func (f *fakeModule) SandboxEnv() (map[string]string, error) {
	if f.envErr != nil {
		return nil, f.envErr
	}

	return f.env, nil
}

type fakeSandboxService struct {
	startCalls int
	stopCalls  int
	startErr   error
	calls      *[]string
}

func (f *fakeSandboxService) Start(context.Context) error {
	f.startCalls++
	*f.calls = append(*f.calls, "sandbox-start")
	return f.startErr
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

func (f *fakeProxyClient) Datasources() serverapi.DatasourcesResponse {
	return serverapi.DatasourcesResponse{}
}

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
	_ module.RuntimeDependencyBinder    = (*fakeModule)(nil)
	_ module.SandboxEnvProvider         = (*fakeModule)(nil)
	_ sandbox.Service                   = (*fakeSandboxService)(nil)
	_ proxy.Service                     = (*fakeProxyClient)(nil)
	_ cartographoor.CartographoorClient = (*fakeCartographoorClient)(nil)
)
