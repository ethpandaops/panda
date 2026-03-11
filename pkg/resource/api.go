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

// APIDocResponse is the response for the python://ethpandaops resource.
type APIDocResponse struct {
	Library     string                     `json:"library"`
	Description string                     `json:"description"`
	Modules     map[string]types.ModuleDoc `json:"modules"`
}

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
			mcp.WithAnnotations([]mcp.Role{mcp.RoleAssistant}, 0.9),
		),
		Handler: createAPIHandler(moduleReg),
	})

	log.Debug("Registered API resources")
}

func createAPIHandler(moduleReg *module.Registry) ReadHandler {
	return func(_ context.Context, _ string) (string, error) {
		modules := moduleReg.PythonAPIDocs()

		// Add platform-owned storage module.
		modules["storage"] = types.ModuleDoc{
			Description: "Upload files to S3-compatible storage for sharing",
			Functions: map[string]types.FunctionDoc{
				"upload": {
					Signature:   "storage.upload(local_path: str, remote_name: str = None) -> str",
					Description: "Upload a local file to S3 and return the public URL",
					Parameters: map[string]string{
						"local_path":  "Path to file (e.g., '/workspace/chart.png')",
						"remote_name": "Optional: custom name in S3",
					},
					Returns: "Public URL string",
				},
				"list_files": {
					Signature:   "storage.list_files(prefix: str = '') -> list[dict]",
					Description: "List files in S3 bucket",
					Returns:     "List of dicts with 'key', 'size', 'last_modified'",
				},
				"get_url": {
					Signature:   "storage.get_url(key: str) -> str",
					Description: "Get public URL for an S3 key",
					Returns:     "Public URL string",
				},
			},
		}

		response := APIDocResponse{
			Library:     "ethpandaops",
			Description: "Data access library for Ethereum network analytics. Import: from ethpandaops import clickhouse, prometheus, loki, storage",
			Modules:     modules,
		}

		data, err := json.MarshalIndent(response, "", "  ")
		if err != nil {
			return "", fmt.Errorf("marshaling API docs: %w", err)
		}

		return string(data), nil
	}
}
