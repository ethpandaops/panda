package resource

import (
	"context"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/surface"
)

func readWorkflow(t *testing.T, uri string, s surface.Dialect) string {
	t.Helper()

	reg := NewRegistry(logrus.New())
	RegisterWorkflowDocsResources(logrus.New(), reg)

	out, mime, err := reg.Read(context.Background(), uri, s)
	require.NoError(t, err)
	require.Equal(t, "text/markdown", mime)

	return out
}

func TestWorkflowGuideCLIDialect(t *testing.T) {
	t.Parallel()

	out := readWorkflow(t, "workflow://guide", surface.CLI)

	require.Contains(t, out, "panda workflow")
	require.Contains(t, out, "Driving workflows")
	require.Contains(t, out, "whiteboard")
}

func TestWorkflowAPICLIDialect(t *testing.T) {
	t.Parallel()

	out := readWorkflow(t, "workflow://api", surface.CLI)

	require.Contains(t, out, "Workflow API cheat-sheet")
	require.Contains(t, out, "/api/v1")
}

func TestWorkflowGuideMCPDialectIsStub(t *testing.T) {
	t.Parallel()

	out := readWorkflow(t, "workflow://guide", surface.MCP)

	require.Contains(t, out, "no MCP interface")
	require.NotContains(t, out, "panda workflow",
		"MCP stub must not teach CLI flags")
}

func TestWorkflowAPIMCPDialectIsStub(t *testing.T) {
	t.Parallel()

	out := readWorkflow(t, "workflow://api", surface.MCP)

	require.Contains(t, out, "no MCP interface")
	require.NotContains(t, out, "panda workflow",
		"MCP stub must not teach CLI flags")
}
