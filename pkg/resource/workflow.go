package resource

import (
	"context"
	"embed"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/surface"
)

//go:embed workflowdocs/*.md
var workflowDocFiles embed.FS

// workflowMCPStub is served for the MCP dialect. The workflow engine has no MCP
// interface, so the stub points MCP clients at the CLI instead of teaching them
// unusable flags.
const workflowMCPStub = "The workflow engine is driven exclusively through the panda CLI; " +
	"there is no MCP interface for it. Use the panda CLI to author, run, and monitor " +
	"workflows.\n"

// RegisterWorkflowDocsResources registers the workflow://guide and workflow://api
// documentation resources. Both are dialect-rendered: the CLI dialect receives
// the full embedded markdown, while the MCP dialect receives a short CLI-only
// stub. Metadata is surface-neutral (it describes the workflow engine, not CLI
// syntax) so MCP resources/list does not leak CLI flavor. These resources are
// intentionally not added to the semantic search index.
func RegisterWorkflowDocsResources(log logrus.FieldLogger, reg Registry) {
	log = log.WithField("resource", "workflow_docs")

	reg.RegisterStatic(StaticResource{
		Resource: mcp.NewResource(
			"workflow://guide",
			"Workflow Engine Guide",
			mcp.WithResourceDescription(
				"Lifecycle guide for the workflow engine: whiteboard, session, "+
					"draft, publish, run, steer, and read outputs."),
			mcp.WithMIMEType("text/markdown"),
		),
		Handler: workflowDocHandler("workflowdocs/guide.md"),
	})

	reg.RegisterStatic(StaticResource{
		Resource: mcp.NewResource(
			"workflow://api",
			"Workflow API Reference",
			mcp.WithResourceDescription(
				"Endpoint cheat-sheet for the workflow engine's REST API."),
			mcp.WithMIMEType("text/markdown"),
		),
		Handler: workflowDocHandler("workflowdocs/api.md"),
	})

	log.Debug("Registered workflow docs resources")
}

// workflowDocHandler returns a read handler that serves the embedded markdown for
// the CLI dialect and the CLI-only stub for every other (MCP) dialect.
func workflowDocHandler(embedPath string) ReadHandler {
	return func(_ context.Context, _ string, s surface.Dialect) (string, error) {
		if s.Key() != surface.CLI.Key() {
			return workflowMCPStub, nil
		}

		data, err := workflowDocFiles.ReadFile(embedPath)
		if err != nil {
			return "", fmt.Errorf("reading workflow doc %s: %w", embedPath, err)
		}

		return string(data), nil
	}
}
