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

// RegisterExamplesResources registers the examples://queries resource.
func RegisterExamplesResources(log logrus.FieldLogger, reg Registry, moduleReg *module.Registry) {
	log = log.WithField("resource", "examples")

	reg.RegisterStatic(StaticResource{
		Resource: mcp.NewResource(
			"examples://queries",
			"Query Examples",
			mcp.WithResourceDescription("Example queries for ClickHouse, Prometheus, and Loki data"),
			mcp.WithMIMEType("application/json"),
			mcp.WithAnnotations([]mcp.Role{mcp.RoleAssistant}, 0.6),
		),
		Handler: createExamplesHandler(moduleReg),
	})

	log.Debug("Registered examples resources")
}

func createExamplesHandler(moduleReg *module.Registry) ReadHandler {
	return func(_ context.Context, _ string) (string, error) {
		examples := moduleReg.Examples()

		data, err := json.MarshalIndent(examples, "", "  ")
		if err != nil {
			return "", fmt.Errorf("marshaling examples: %w", err)
		}

		return string(data), nil
	}
}

// GetQueryExamples returns query examples from initialized modules only.
func GetQueryExamples(moduleReg *module.Registry) map[string]types.ExampleCategory {
	return moduleReg.Examples()
}
