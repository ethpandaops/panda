package surface

import (
	"fmt"
	"sort"
	"strings"
)

// mcpDialect speaks the MCP dialect: tool calls and resource URIs.
type mcpDialect struct{}

var _ Dialect = mcpDialect{}

// Key returns the wire identifier for the MCP surface.
func (mcpDialect) Key() string { return "mcp" }

// ExecuteRef returns the inline reference to the execute_python tool.
func (mcpDialect) ExecuteRef() string { return "`execute_python`" }

// PythonBlock renders python source as a fenced python block.
func (mcpDialect) PythonBlock(code string) string {
	return "```python\n" + code + "\n```\n"
}

// PythonBlockInSession renders python source for a reused session. MCP
// clients resume sessions via the session_id tool argument, not in code,
// so the block is identical to PythonBlock.
func (s mcpDialect) PythonBlockInSession(code string) string {
	return s.PythonBlock(code)
}

// SessionHint says how MCP clients reuse a sandbox session.
func (mcpDialect) SessionHint() string {
	return "Pass `session_id` from tool responses for file persistence and faster startup"
}

// SearchExamples returns a search tool invocation scoped to a dataset.
func (mcpDialect) SearchExamples(dataset, topic string) string {
	return fmt.Sprintf("the search tool: `search(type=\"examples\", dataset=%q, query=%q)`", dataset, topic)
}

// ResourceRef returns the resource URI as an inline reference.
func (mcpDialect) ResourceRef(uri string) string {
	return "`" + uri + "`"
}

// PythonDocsRef returns the Python API docs resource reference.
func (mcpDialect) PythonDocsRef(_ string) string {
	return "`python://ethpandaops`"
}

// GettingStartedIntro returns the MCP onboarding workflow section. It
// teaches the system only: no dataset, datasource, table, or network is
// ever named here — those facts live behind the resources it points at.
func (mcpDialect) GettingStartedIntro() string {
	return `## Workflow

1. **Discover** → ` + "`datasets://list`" + ` shows the datasets this deployment holds; ` + "`datasources://list`" + ` shows the connections that hold them (placement params + operator notes); ` + "`networks://active`" + ` shows live network/devnet ids
2. **Learn the data** → **read ` + "`datasets://{name}`" + ` before querying a dataset** — its required syntax rules and placement live there. Live schema: ` + "`clickhouse://tables/{cluster}/{database}`" + `
3. **Find patterns** → Use the ` + "`search`" + ` tool to find relevant examples and procedures:
   - ` + "`search(query=\"...\")`" + ` → Search everything (examples, runbooks, EIPs, consensus specs)
   - ` + "`search(type=\"examples\", query=\"...\", dataset=\"...\")`" + ` → Query snippets, optionally scoped to one dataset
   - ` + "`search(type=\"runbooks\", query=\"...\")`" + ` → Investigation procedures only
   - ` + "`search(type=\"consensus-specs\", query=\"...\")`" + ` → Consensus-specs documents and protocol constants
4. **Execute** → ` + "`execute_python`" + ` tool with the ethpandaops library; Python module APIs: ` + "`python://ethpandaops`" + `

`
}

// DiscoveryGuide lists the registered tools and resources.
func (mcpDialect) DiscoveryGuide(d Discovery) string {
	var sb strings.Builder

	sb.WriteString("## Available Tools\n\n")

	for _, tool := range sortedByName(d.Tools) {
		fmt.Fprintf(&sb, "- **%s**: %s\n", tool.Name, firstLine(tool.Description))
	}

	sb.WriteString("\n## Available Resources\n\n")

	for _, res := range sortedByName(d.Resources) {
		fmt.Fprintf(&sb, "- `%s` - %s\n", res.Name, res.Description)
	}

	if len(d.Templates) > 0 {
		sb.WriteString("\n**Templates:**\n")

		for _, tmpl := range sortedByName(d.Templates) {
			fmt.Fprintf(&sb, "- `%s` - %s\n", tmpl.Name, tmpl.Description)
		}
	}

	return sb.String()
}

// sortedByName returns a copy of items ordered by name.
func sortedByName(items []Item) []Item {
	sorted := make([]Item, len(items))
	copy(sorted, items)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Name < sorted[j].Name
	})

	return sorted
}

// firstLine truncates a description to its first non-empty line.
func firstLine(desc string) string {
	if idx := strings.Index(desc, "\n"); idx > 0 {
		desc = desc[:idx]
	}

	return strings.TrimSpace(desc)
}
