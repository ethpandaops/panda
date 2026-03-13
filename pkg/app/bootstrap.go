package app

import (
	"context"
	"fmt"

	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/proxy"
)

// BootstrapOptions controls which optional runtime services are started.
type BootstrapOptions struct {
	StartSandbox       bool
	StartCartographoor bool
}

// Bootstrap initializes the application in dependency order:
// module registry -> sandbox -> proxy -> module init/start -> cartographoor.
func (a *App) Bootstrap(ctx context.Context, opts BootstrapOptions) error {
	a.log.Info("Building application dependencies")

	moduleReg, err := a.moduleRegistryBuilder()
	if err != nil {
		return fmt.Errorf("building module registry: %w", err)
	}

	a.ModuleRegistry = moduleReg

	if opts.StartSandbox {
		if err := a.startSandbox(ctx); err != nil {
			return err
		}
	}

	if err := a.startProxy(ctx); err != nil {
		a.stop(ctx)

		return err
	}

	if err := a.initModules(a.ProxyService); err != nil {
		a.stop(ctx)

		return fmt.Errorf("initializing modules: %w", err)
	}

	a.bindRuntimeDependencies()

	if err := a.ModuleRegistry.StartAll(ctx); err != nil {
		a.stop(ctx)

		return fmt.Errorf("starting modules: %w", err)
	}

	a.log.Info("All modules started")

	if !opts.StartCartographoor {
		return nil
	}

	if err := a.startCartographoor(ctx); err != nil {
		a.stop(ctx)

		return err
	}

	a.bindRuntimeDependencies()

	return nil
}

func (a *App) startSandbox(ctx context.Context) error {
	sandboxSvc, err := a.sandboxBuilder()
	if err != nil {
		return fmt.Errorf("building sandbox: %w", err)
	}

	if err := sandboxSvc.Start(ctx); err != nil {
		return fmt.Errorf("starting sandbox: %w", err)
	}

	a.Sandbox = sandboxSvc
	a.log.WithField("backend", sandboxSvc.Name()).Info("Sandbox service started")

	return nil
}

func (a *App) startProxy(ctx context.Context) error {
	proxySvc := a.proxyServiceBuilder()
	if err := proxySvc.Start(ctx); err != nil {
		return fmt.Errorf("starting proxy client: %w", err)
	}

	a.ProxyService = proxySvc
	a.log.WithField("url", proxySvc.URL()).Info("Proxy client connected")

	return nil
}

func (a *App) startCartographoor(ctx context.Context) error {
	cartographoorClient := a.cartographoorBuilder()
	if err := cartographoorClient.Start(ctx); err != nil {
		return fmt.Errorf("starting cartographoor client: %w", err)
	}

	a.Cartographoor = cartographoorClient
	a.log.Info("Cartographoor client started")

	return nil
}

func (a *App) bindRuntimeDependencies() {
	if a.ModuleRegistry == nil {
		return
	}

	var proxySchemaAccess proxy.ClickHouseSchemaAccess
	if schemaAccess, ok := a.ProxyService.(proxy.ClickHouseSchemaAccess); ok {
		proxySchemaAccess = schemaAccess
	}

	a.ModuleRegistry.BindRuntimeDependencies(module.RuntimeDependencies{
		ProxySchemaAccess: proxySchemaAccess,
		Cartographoor:     a.Cartographoor,
	})
}
