// Package proxy provides the credential proxy for server-side upstream access.
// The proxy holds datasource credentials and serves raw credentialed routes.
package proxy

import (
	"context"
	"net/http"

	"github.com/ethpandaops/panda/pkg/serverapi"
)

// OutboundAuthorizer attaches proxy authentication to outbound HTTP requests.
type OutboundAuthorizer interface {
	AuthorizeRequest(req *http.Request) error
}

// ClickHouseSchemaAccess is the narrow proxy contract used for ClickHouse
// schema discovery.
type ClickHouseSchemaAccess interface {
	URL() string

	OutboundAuthorizer

	ClickHouseDatasources() []string
}

// DatasourceDiscoverer refreshes datasource metadata from a remote proxy.
type DatasourceDiscoverer interface {
	Discover(ctx context.Context) error
}

// AuthenticationChecker verifies local credentials for the proxy control plane.
type AuthenticationChecker interface {
	EnsureAuthenticated(ctx context.Context) error
}

// DatasourceCatalog exposes the last known proxy datasource snapshot.
type DatasourceCatalog interface {
	Datasources() serverapi.DatasourcesResponse
}

// RuntimeTokens exposes execution-token wiring for runtime-authenticated server routes.
type RuntimeTokens interface {
	RegisterToken(executionID string) string
	RevokeToken(executionID string)
}

// Service is the shared proxy boundary used by the app and MCP server.
// Client-only behaviors such as discovery and credential checks live on
// narrower optional interfaces.
type Service interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	URL() string
	OutboundAuthorizer
	RuntimeTokens
	DatasourceCatalog
}
