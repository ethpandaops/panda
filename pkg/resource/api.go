package resource

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/serverapi"
	"github.com/ethpandaops/panda/pkg/surface"
	"github.com/ethpandaops/panda/pkg/types"
)

// RegisterAPIResources registers the python://ethpandaops resource
// with the registry.
func RegisterAPIResources(log logrus.FieldLogger, reg Registry, moduleReg *module.Registry) {
	log = log.WithField("resource", "api")

	reg.RegisterStatic(StaticResource{
		Resource: mcp.NewResource(
			"python://ethpandaops",
			"ethpandaops Python Library API",
			mcp.WithResourceDescription("API documentation for the ethpandaops Python library"),
			mcp.WithMIMEType("application/json"),
			mcp.WithAnnotations([]mcp.Role{mcp.RoleAssistant}, 0.9, ""),
		),
		Handler: createAPIHandler(moduleReg),
	})

	log.Debug("Registered API resources")
}

func createAPIHandler(moduleReg *module.Registry) ReadHandler {
	return func(_ context.Context, _ string, _ surface.Dialect) (string, error) {
		modules := moduleReg.PythonAPIDocs()

		// Add platform-owned storage module.
		modules["storage"] = types.ModuleDoc{
			Description: "Upload files to storage for sharing",
			Functions: map[string]types.FunctionDoc{
				"upload": {
					Signature:   "storage.upload(local_path: str, remote_name: str = None) -> str",
					Description: "Upload a local file to storage and return the public URL",
					Parameters: map[string]string{
						"local_path":  "Path to file (e.g., '/workspace/chart.png')",
						"remote_name": "Optional: custom name for the stored file",
					},
					Returns: "Public URL string",
				},
				"list_files": {
					Signature:   "storage.list_files(prefix: str = '') -> list[dict]",
					Description: "List uploaded files",
					Returns:     "List of dicts with 'key', 'size', 'last_modified'",
				},
				"get_url": {
					Signature:   "storage.get_url(key: str) -> str",
					Description: "Get public URL for a stored file",
					Returns:     "Public URL string",
				},
			},
		}
		modules["specs"] = types.ModuleDoc{
			Description: "Read consensus-specs protocol constants and documents",
			Functions: map[string]types.FunctionDoc{
				"get_constant": {
					Signature:   "specs.get_constant(name: str, fork: str | None = None) -> dict",
					Description: "Get a consensus-specs protocol constant by name",
					Parameters: map[string]string{
						"name": "Constant name, case-insensitive",
						"fork": "Optional consensus fork filter; when omitted, returns the latest fork that defines the constant",
					},
					Returns: "Dict with 'name', 'value', and 'fork'",
				},
				"list_constants": {
					Signature:   "specs.list_constants(fork: str | None = None, prefix: str | None = None) -> list[dict]",
					Description: "List consensus-specs protocol constants",
					Parameters: map[string]string{
						"fork":   "Optional consensus fork filter",
						"prefix": "Optional case-insensitive constant name prefix filter",
					},
					Returns: "List of dicts with 'name', 'value', and 'fork'",
				},
				"get_spec": {
					Signature:   "specs.get_spec(fork: str, topic: str) -> dict",
					Description: "Get a consensus spec document",
					Parameters: map[string]string{
						"fork":  "Consensus fork name",
						"topic": "Spec topic name",
					},
					Returns: "Dict with 'fork', 'topic', 'title', 'content', and 'url'",
				},
			},
		}

		response := serverapi.APIDocResponse{
			Library:     "ethpandaops",
			Description: "Data access library for Ethereum network analytics. Import: from ethpandaops import clickhouse, prometheus, loki, specs, storage",
			Modules:     modules,
		}

		data, err := json.MarshalIndent(response, "", "  ")
		if err != nil {
			return "", fmt.Errorf("marshaling API docs: %w", err)
		}

		return string(data), nil
	}
}
