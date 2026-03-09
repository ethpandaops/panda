package resource

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/mcp/pkg/extension"
	"github.com/ethpandaops/mcp/pkg/types"
)

// RegisterExamplesResources registers the examples://queries resource.
func RegisterExamplesResources(log logrus.FieldLogger, reg Registry, extensionReg *extension.Registry) {
	log = log.WithField("resource", "examples")

	reg.RegisterStatic(StaticResource{
		Resource: mcp.NewResource(
			"examples://queries",
			"Query Examples",
			mcp.WithResourceDescription("Example queries for ClickHouse, Prometheus, and Loki data"),
			mcp.WithMIMEType("application/json"),
			mcp.WithAnnotations([]mcp.Role{mcp.RoleAssistant}, 0.6),
		),
		Handler: createExamplesHandler(extensionReg),
	})

	log.Debug("Registered examples resources")
}

func createExamplesHandler(extensionReg *extension.Registry) ReadHandler {
	return func(_ context.Context, _ string) (string, error) {
		// Use AllExamples to include examples from all extensions,
		// not just initialized ones (examples don't need credentials).
		examples := extensionReg.AllExamples()

		data, err := json.MarshalIndent(examples, "", "  ")
		if err != nil {
			return "", fmt.Errorf("marshaling examples: %w", err)
		}

		return string(data), nil
	}
}

// GetQueryExamples returns all query examples from ALL registered extensions,
// regardless of initialization status. Examples are static embedded data.
func GetQueryExamples(extensionReg *extension.Registry) map[string]types.ExampleCategory {
	return extensionReg.AllExamples()
}
