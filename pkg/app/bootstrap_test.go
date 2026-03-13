package app

import (
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/config"
)

func TestBindRuntimeDependenciesWithoutRegistryIsNoOp(t *testing.T) {
	t.Parallel()

	app := New(logrus.New(), &config.Config{})
	app.ProxyService = &fakeProxyClient{url: "http://proxy", calls: &[]string{}}
	app.Cartographoor = &fakeCartographoorClient{calls: &[]string{}}

	app.bindRuntimeDependencies()
}

func TestBindRuntimeDependenciesInjectsCurrentRuntimeServices(t *testing.T) {
	t.Parallel()

	logger := logrus.New()
	calls := &[]string{}
	moduleStub := &fakeModule{name: "fake", calls: calls}
	proxyClient := &fakeProxyClient{url: "http://proxy", calls: calls}
	cartClient := &fakeCartographoorClient{calls: calls}

	app := New(logger, &config.Config{})
	app.ModuleRegistry = newInitializedRegistry(t, logger, moduleStub)
	app.ProxyService = proxyClient
	app.Cartographoor = cartClient

	app.bindRuntimeDependencies()

	require.Same(t, proxyClient, moduleStub.proxyClient)
	require.Same(t, cartClient, moduleStub.cartClient)
	assert.Contains(t, *calls, "proxy-injected")
	assert.Contains(t, *calls, "cart-injected")
}
