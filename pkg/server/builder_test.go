package server

import (
	"context"
	"errors"
	"testing"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/app"
	"github.com/ethpandaops/panda/pkg/config"
	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/resource"
	"github.com/ethpandaops/panda/pkg/searchruntime"
)

type builderProxyClientStub struct {
	*coverageProxyService
}

func newStubBuilderApp(t *testing.T) *app.App {
	t.Helper()

	proxySvc := &coverageProxyService{url: "https://proxy.example"}

	return &app.App{
		ModuleRegistry: newInitializedModuleRegistry(t),
		Sandbox:        coverageSandboxService{name: "stub"},
		ProxyService:   &builderProxyClientStub{coverageProxyService: proxySvc},
		Cartographoor:  &stubCartographoorClient{},
	}
}

func TestBuilderHelpersRegisterSearchAndResources(t *testing.T) {
	t.Parallel()

	builder := NewBuilder(logrus.New(), &config.Config{
		Proxy: config.ProxyConfig{
			URL: "https://proxy.example/",
			Auth: &config.ProxyAuthConfig{
				IssuerURL: "https://issuer.example/",
				ClientID:  "client-id",
			},
		},
	})

	toolReg := builder.buildToolRegistry(
		coverageSandboxService{name: "stub"},
		nil,
		&resource.ExampleIndex{},
		newInitializedModuleRegistry(t),
		nil,
		nil,
	)
	if got := len(toolReg.List()); got != 3 {
		t.Fatalf("buildToolRegistry() len = %d, want 3 including search", got)
	}

	resourceReg := builder.buildResourceRegistry(nil, &coverageProxyService{}, newInitializedModuleRegistry(t), toolReg)
	if got := len(resourceReg.ListStatic()); got == 0 {
		t.Fatal("buildResourceRegistry() registered no static resources")
	}

	meta := buildProxyAuthMetadata(builder.cfg)
	if !meta.Enabled {
		t.Fatalf("buildProxyAuthMetadata() = %#v, want enabled metadata", meta)
	}
	if meta.IssuerURL != "https://issuer.example/" {
		t.Fatalf("IssuerURL = %q, want explicit issuer URL", meta.IssuerURL)
	}
	if meta.Resource != "https://proxy.example" {
		t.Fatalf("Resource = %q, want trimmed proxy URL", meta.Resource)
	}
}

func TestBuilderBuildReturnsBootstrapErrors(t *testing.T) {
	t.Parallel()

	builder := NewBuilder(logrus.New(), &config.Config{
		Server: config.ServerConfig{
			Port: 2480,
		},
		Sandbox: config.SandboxConfig{
			Backend: "definitely-not-supported",
		},
		Proxy: config.ProxyConfig{
			URL: "https://proxy.example",
		},
	})

	svc, err := builder.Build(context.Background())
	if err == nil || err.Error() != "building sandbox: unsupported sandbox backend: definitely-not-supported" {
		t.Fatalf("Build() error = %v, want wrapped sandbox bootstrap error", err)
	}
	if svc != nil {
		t.Fatalf("Build() service = %#v, want nil", svc)
	}
}

func TestBuilderBuildStopsApplicationWhenSearchRuntimeFails(t *testing.T) {
	t.Parallel()

	builder := NewBuilder(logrus.New(), &config.Config{
		Server: config.ServerConfig{Port: 2480},
		Proxy:  config.ProxyConfig{URL: "https://proxy.example"},
	})
	stubApp := newStubBuilderApp(t)
	stopCalls := 0
	builder.newApplication = func(logrus.FieldLogger, *config.Config) *app.App {
		return stubApp
	}
	builder.bootstrapApplication = func(context.Context, *app.App) error { return nil }
	builder.stopApplication = func(context.Context, *app.App) error {
		stopCalls++
		return nil
	}
	builder.buildSearchRuntime = func(logrus.FieldLogger, config.SemanticSearchConfig, *module.Registry) (*searchruntime.Runtime, error) {
		return nil, errors.New("search failed")
	}

	svc, err := builder.Build(context.Background())
	if err == nil || err.Error() != "building search runtime: search failed" {
		t.Fatalf("Build() error = %v, want wrapped search failure", err)
	}
	if svc != nil {
		t.Fatalf("Build() service = %#v, want nil", svc)
	}
	if stopCalls != 1 {
		t.Fatalf("stopCalls = %d, want 1 after search runtime failure", stopCalls)
	}
}

func TestBuilderBuildSucceedsWithoutSearchRuntimeAndCleanupDoesNotPanic(t *testing.T) {
	t.Parallel()

	builder := NewBuilder(logrus.New(), &config.Config{
		Server: config.ServerConfig{Port: 2480},
		Proxy:  config.ProxyConfig{URL: "https://proxy.example"},
	})
	stubApp := newStubBuilderApp(t)
	stopCalls := 0
	builder.newApplication = func(logrus.FieldLogger, *config.Config) *app.App {
		return stubApp
	}
	builder.bootstrapApplication = func(context.Context, *app.App) error { return nil }
	builder.stopApplication = func(context.Context, *app.App) error {
		stopCalls++
		return nil
	}
	builder.buildSearchRuntime = func(logrus.FieldLogger, config.SemanticSearchConfig, *module.Registry) (*searchruntime.Runtime, error) {
		return nil, nil
	}

	svc, err := builder.Build(context.Background())
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	srv, ok := svc.(*service)
	if !ok {
		t.Fatalf("Build() service = %T, want *service", svc)
	}
	if srv.storageService == nil {
		t.Fatal("storageService = nil, want initialized storage service")
	}
	if err := srv.cleanup(context.Background()); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}
	if stopCalls != 1 {
		t.Fatalf("stopCalls = %d, want cleanup to stop application once", stopCalls)
	}
}
