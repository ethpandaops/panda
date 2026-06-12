package surface

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFromKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want Dialect
	}{
		{name: "cli key", key: "cli", want: CLI},
		{name: "mcp key", key: "mcp", want: MCP},
		{name: "empty defaults to mcp", key: "", want: MCP},
		{name: "unknown defaults to mcp", key: "browser", want: MCP},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, FromKey(tt.key))
		})
	}
}

func TestResourceRef(t *testing.T) {
	tests := []struct {
		name string
		s    Dialect
		uri  string
		want string
	}{
		{name: "mcp is the uri", s: MCP, uri: "datasources://clickhouse", want: "`datasources://clickhouse`"},
		{name: "cli generic form", s: CLI, uri: "datasources://clickhouse", want: "`panda resources datasources://clickhouse`"},
		{name: "cli dedicated datasets command", s: CLI, uri: "datasets://list", want: "`panda datasets`"},
		{name: "cli dedicated docs command", s: CLI, uri: "python://ethpandaops", want: "`panda docs`"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.s.ResourceRef(tt.uri))
		})
	}
}

func TestSearchExamples(t *testing.T) {
	assert.Equal(t,
		`the search tool: `+"`"+`search(type="examples", dataset="otel-logs", query="<topic>")`+"`",
		MCP.SearchExamples("otel-logs", "<topic>"))
	assert.Equal(t,
		"`panda search examples --dataset otel-logs \"<topic>\"`",
		CLI.SearchExamples("otel-logs", "<topic>"))
}

func TestPythonBlocks(t *testing.T) {
	code := "print(1)"

	assert.Equal(t, "```python\nprint(1)\n```\n", MCP.PythonBlock(code))
	assert.Equal(t, MCP.PythonBlock(code), MCP.PythonBlockInSession(code),
		"MCP resumes sessions via session_id tool argument, not in code")

	assert.Equal(t, "```\npanda execute <<'PY'\nprint(1)\nPY\n```\n", CLI.PythonBlock(code))
	assert.Equal(t, "```\npanda execute --session <id> <<'PY'\nprint(1)\nPY\n```\n",
		CLI.PythonBlockInSession(code))
}

func TestCLIGuideDiscouragesGuessing(t *testing.T) {
	guide := CLI.GettingStartedIntro()

	assert.Contains(t, guide, "Do not guess command names")
	assert.Contains(t, guide, "examples and runbooks first")
	assert.Contains(t, guide, "panda search consensus-specs")
}

func TestDiscoveryGuide(t *testing.T) {
	d := Discovery{
		Tools: []Item{
			{Name: "search", Description: "Semantic search\nsecond line ignored"},
			{Name: "execute_python", Description: "Run python"},
		},
		Resources: []Item{
			{Name: "datasets://list", Description: "Datasets"},
		},
		Templates: []Item{
			{Name: "datasets://{name}", Description: "Dataset Guide"},
		},
	}

	mcpGuide := MCP.DiscoveryGuide(d)
	assert.Contains(t, mcpGuide, "- **execute_python**: Run python")
	assert.Contains(t, mcpGuide, "- **search**: Semantic search")
	assert.NotContains(t, mcpGuide, "second line ignored")
	assert.Contains(t, mcpGuide, "- `datasets://list` - Datasets")
	assert.Contains(t, mcpGuide, "- `datasets://{name}` - Dataset Guide")
	assert.Less(t, // tools are sorted by name
		strings.Index(mcpGuide, "execute_python"), strings.Index(mcpGuide, "**search**"))

	cliGuide := CLI.DiscoveryGuide(d)
	assert.Contains(t, cliGuide, "panda --help")
	assert.Contains(t, cliGuide, "panda search consensus-specs")
	assert.Contains(t, cliGuide, "panda search runbooks")
}
