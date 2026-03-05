// Package proxy provides the credential proxy for sandboxed code execution.
// The proxy holds datasource credentials and proxies requests from sandbox containers.
package proxy

import (
	"context"

	"github.com/ethpandaops/mcp/pkg/types"
)

// Service is the credential proxy service interface.
// This is implemented by both Client (for connecting to a proxy)
// and directly by the proxy Server.
type Service interface {
	// Start starts the service.
	Start(ctx context.Context) error

	// Stop stops the service.
	Stop(ctx context.Context) error

	// URL returns the proxy URL for sandbox environment variables.
	URL() string

	// RegisterToken creates a new token for an execution and returns it.
	// For Client implementations using JWT auth, this returns the user's JWT.
	RegisterToken(executionID string) string

	// RevokeToken revokes a token for an execution.
	// For Client implementations using JWT auth, this is a no-op.
	RevokeToken(executionID string)

	// ClickHouseDatasources returns the list of ClickHouse datasource names.
	ClickHouseDatasources() []string
	// ClickHouseDatasourceInfo returns detailed ClickHouse datasource info.
	ClickHouseDatasourceInfo() []types.DatasourceInfo

	// PrometheusDatasources returns the list of Prometheus datasource names.
	PrometheusDatasources() []string
	// PrometheusDatasourceInfo returns detailed Prometheus datasource info.
	PrometheusDatasourceInfo() []types.DatasourceInfo

	// LokiDatasources returns the list of Loki datasource names.
	LokiDatasources() []string
	// LokiDatasourceInfo returns detailed Loki datasource info.
	LokiDatasourceInfo() []types.DatasourceInfo

	// S3Bucket returns the configured S3 bucket name.
	S3Bucket() string

	// S3PublicURLPrefix returns the public URL prefix for S3 objects.
	S3PublicURLPrefix() string

	// EthNodeAvailable returns true if ethnode proxy access is configured.
	EthNodeAvailable() bool
}
