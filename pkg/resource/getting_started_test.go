package resource

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/surface"
)

type fakeToolLister struct {
	tools []mcp.Tool
}

func (f *fakeToolLister) List() []mcp.Tool { return f.tools }

func renderGettingStarted(t *testing.T, s surface.Dialect) string {
	t.Helper()

	reg := NewRegistry(logrus.New())
	toolReg := &fakeToolLister{tools: []mcp.Tool{
		{Name: "execute_python", Description: "Run python in the sandbox"},
		{Name: "search", Description: "Semantic search"},
	}}

	RegisterGettingStartedResources(logrus.New(), reg, toolReg)

	out, _, err := reg.Read(context.Background(), "panda://getting-started", s)
	require.NoError(t, err)

	return out
}

func TestGettingStartedMCPDialect(t *testing.T) {
	out := renderGettingStarted(t, surface.MCP)

	require.Contains(t, out, "# Getting Started Guide")
	require.Contains(t, out, "`execute_python`")
	require.Contains(t, out, "## Available Tools")
	require.Contains(t, out, "- **execute_python**: Run python in the sandbox")
	require.Contains(t, out, "```python")
	require.Contains(t, out, "`session_id`")
	require.Contains(t, out, "`python://ethpandaops`")
	require.NotContains(t, out, "panda execute --session",
		"MCP guide must not teach CLI flags")
}

func TestGettingStartedCLIDialect(t *testing.T) {
	out := renderGettingStarted(t, surface.CLI)

	require.Contains(t, out, "# Getting Started Guide")
	require.Contains(t, out, "panda execute <<'PY'")
	require.Contains(t, out, "panda execute --session <id> <<'PY'")
	require.Contains(t, out, "`--session <id>`")
	require.Contains(t, out, "`panda docs storage`")
	require.Contains(t, out, "panda --help")
	require.NotContains(t, out, "## Available Tools",
		"CLI guide documents commands, not MCP tools")
	require.NotContains(t, out, "session_id",
		"CLI guide must not teach MCP tool arguments")
}
