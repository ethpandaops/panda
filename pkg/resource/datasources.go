package resource

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/types"
)

// DatasourcesJSONResponse is the JSON response for datasources resources.
type DatasourcesJSONResponse struct {
	Datasources []types.DatasourceInfo `json:"datasources"`
}

// DatasourceProvider provides datasource information from the module registry.
type DatasourceProvider struct {
	moduleReg *module.Registry
}

// NewDatasourceProvider creates a new datasource provider.
func NewDatasourceProvider(moduleReg *module.Registry) *DatasourceProvider {
	return &DatasourceProvider{
		moduleReg: moduleReg,
	}
}

// DatasourceInfo returns aggregated datasource info from all initialized modules.
func (p *DatasourceProvider) DatasourceInfo() []types.DatasourceInfo {
	if p.moduleReg == nil {
		return nil
	}

	return p.moduleReg.DatasourceInfo()
}

// RegisterDatasourcesResources registers the datasources:// resources
// with the registry.
func RegisterDatasourcesResources(
	log logrus.FieldLogger,
	reg Registry,
	moduleReg *module.Registry,
) {
	log = log.WithField("resource", "datasources")
	provider := NewDatasourceProvider(moduleReg)

	// datasources://list - all datasources
	reg.RegisterStatic(StaticResource{
		Resource: mcp.NewResource(
			"datasources://list",
			"All Datasources",
			mcp.WithResourceDescription("List of all configured datasources (ClickHouse, Prometheus, Loki)"),
			mcp.WithMIMEType("application/json"),
			mcp.WithAnnotations([]mcp.Role{mcp.RoleAssistant}, 0.8),
		),
		Handler: createDatasourcesHandler(provider, ""),
	})

	// datasources://clickhouse
	reg.RegisterStatic(StaticResource{
		Resource: mcp.NewResource(
			"datasources://clickhouse",
			"ClickHouse Datasources",
			mcp.WithResourceDescription("Configured ClickHouse clusters for blockchain data queries"),
			mcp.WithMIMEType("application/json"),
			mcp.WithAnnotations([]mcp.Role{mcp.RoleAssistant}, 0.7),
		),
		Handler: createDatasourcesHandler(provider, "clickhouse"),
	})

	// datasources://prometheus
	reg.RegisterStatic(StaticResource{
		Resource: mcp.NewResource(
			"datasources://prometheus",
			"Prometheus Datasources",
			mcp.WithResourceDescription("Configured Prometheus instances for metrics queries"),
			mcp.WithMIMEType("application/json"),
			mcp.WithAnnotations([]mcp.Role{mcp.RoleAssistant}, 0.7),
		),
		Handler: createDatasourcesHandler(provider, "prometheus"),
	})

	// datasources://loki
	reg.RegisterStatic(StaticResource{
		Resource: mcp.NewResource(
			"datasources://loki",
			"Loki Datasources",
			mcp.WithResourceDescription("Configured Loki instances for log queries"),
			mcp.WithMIMEType("application/json"),
			mcp.WithAnnotations([]mcp.Role{mcp.RoleAssistant}, 0.7),
		),
		Handler: createDatasourcesHandler(provider, "loki"),
	})

	log.Debug("Registered datasources resources")
}

func createDatasourcesHandler(provider *DatasourceProvider, filterType string) ReadHandler {
	return func(_ context.Context, _ string) (string, error) {
		allInfos := provider.DatasourceInfo()

		var filtered []types.DatasourceInfo
		if filterType == "" {
			if allInfos == nil {
				filtered = make([]types.DatasourceInfo, 0)
			} else {
				filtered = allInfos
			}
		} else {
			filtered = make([]types.DatasourceInfo, 0, len(allInfos))
			for _, info := range allInfos {
				if info.Type == filterType {
					filtered = append(filtered, info)
				}
			}
		}

		response := DatasourcesJSONResponse{Datasources: filtered}

		data, err := json.MarshalIndent(response, "", "  ")
		if err != nil {
			return "", fmt.Errorf("marshaling datasources: %w", err)
		}

		return string(data), nil
	}
}
