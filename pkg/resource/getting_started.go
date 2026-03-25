package resource

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/types"
)

// ToolLister provides access to registered tools.
type ToolLister interface {
	List() []mcp.Tool
}

// gettingStartedHeaderMCP contains the MCP workflow guidance.
const gettingStartedHeaderMCP = `# Getting Started Guide

## Workflow

1. **Discover** → Read datasource resources to find available data sources and schemas
2. **Find patterns** → Use the ` + "`search`" + ` tool to find relevant examples and procedures:
   - ` + "`search(query=\"...\")`" + ` → Search everything (examples, runbooks, EIPs, consensus specs)
   - ` + "`search(type=\"examples\", query=\"...\")`" + ` → Query snippets only
   - ` + "`search(type=\"runbooks\", query=\"...\")`" + ` → Investigation procedures only
   - ` + "`search(type=\"consensus-specs\", query=\"...\")`" + ` → Consensus-specs documents and protocol constants
3. **Execute** → ` + "`execute_python`" + ` tool with the ethpandaops library

`

// gettingStartedHeaderCLI contains the CLI workflow guidance.
const gettingStartedHeaderCLI = `# Getting Started Guide

## Preferred Workflow: panda execute

**Always use ` + "`panda execute`" + ` for data analysis.** This is the Python sandbox — the same
engine used by MCP clients via ` + "`execute_python`" + `. It provides:

- **Workspace persistence** between calls (files saved to ` + "`/workspace/`" + ` survive across executions)
- **Multi-turn workflows** (query → save → load → plot across separate calls)
- **Token efficiency** (one command handles any datasource type)
- **Full ethpandaops library** (clickhouse, prometheus, loki, dora, ethnode, storage)

While module-specific CLI commands exist (e.g. ` + "`panda clickhouse query`" + `), **prefer
` + "`panda execute`" + `** because it supports multi-step workflows with workspace persistence
that module commands cannot provide.

### Quick Start

` + "```" + `
panda execute --code '
from ethpandaops import clickhouse
df = clickhouse.query("xatu-cbt", """
    SELECT slot, proposer_index
    FROM mainnet.fct_block_canonical
    ORDER BY slot DESC
    LIMIT 5
""")
print(df)
'
` + "```" + `

### Discovery

Run ` + "`panda --help`" + ` to see all available commands and ` + "`panda resources`" + ` to list
available data resources. Use ` + "`panda <command> --help`" + ` for details on any command.

`

// gettingStartedFooterMCP contains MCP-specific tips.
const gettingStartedFooterMCP = `
## Sessions

**IMPORTANT:** Each ` + "`execute_python`" + ` call runs in a **fresh Python process**. Variables do NOT persist between calls.

- **Files persist**: Save to ` + "`/workspace/`" + ` to share data between executions
- **Variables do NOT persist**: ` + "`df`" + ` from one call won't exist in the next
- **Reuse session_id**: Pass it from tool responses for file persistence and faster startup

**Example - Multi-step workflow:**
` + "```python" + `
# Call 1: Query and SAVE to workspace
df = clickhouse.query("xatu-cbt", "SELECT ...")
df.to_parquet("/workspace/data.parquet")  # Persist!
` + "```" + `

` + "```python" + `
# Call 2: LOAD from workspace and plot
import pandas as pd
df = pd.read_parquet("/workspace/data.parquet")  # Load!
plt.plot(df["time"], df["value"])
plt.savefig("/workspace/chart.png")
url = storage.upload("/workspace/chart.png")
` + "```" + `

Use ` + "`storage.upload()`" + ` for permanent public URLs (see ` + "`python://ethpandaops`" + ` for API details).
`

// gettingStartedFooterCLI contains CLI-specific tips.
const gettingStartedFooterCLI = `
## Sessions

**IMPORTANT:** Each ` + "`panda execute`" + ` call runs in a **fresh Python process**. Variables do NOT persist between calls.

- **Files persist**: Save to ` + "`/workspace/`" + ` to share data between executions
- **Variables do NOT persist**: ` + "`df`" + ` from one call won't exist in the next
- **Reuse session**: Pass ` + "`--session <id>`" + ` for file persistence and faster startup

**Example — Multi-step workflow:**
` + "```" + `
panda execute --code '
df = clickhouse.query("xatu-cbt", "SELECT ...")
df.to_parquet("/workspace/data.parquet")
'
` + "```" + `

` + "```" + `
panda execute --code '
import pandas as pd
df = pd.read_parquet("/workspace/data.parquet")
plt.plot(df["time"], df["value"])
plt.savefig("/workspace/chart.png")
url = storage.upload("/workspace/chart.png")
print(url)
'
` + "```" + `

Use ` + "`storage.upload()`" + ` for permanent public URLs (see ` + "`panda docs storage`" + ` for API details).
`

// RegisterGettingStartedResources registers the panda://getting-started
// resource.
func RegisterGettingStartedResources(
	log logrus.FieldLogger,
	reg Registry,
	toolReg ToolLister,
	moduleReg *module.Registry,
) {
	log = log.WithField("resource", "getting_started")

	reg.RegisterStatic(StaticResource{
		Resource: mcp.NewResource(
			"panda://getting-started",
			"Getting Started Guide",
			mcp.WithResourceDescription("Essential guide for querying data - read this first!"),
			mcp.WithMIMEType("text/markdown"),
			mcp.WithAnnotations([]mcp.Role{mcp.RoleAssistant}, 1.0),
		),
		Handler: createGettingStartedHandler(reg, toolReg, moduleReg),
	})

	log.Debug("Registered getting-started resource")
}

// createGettingStartedHandler creates a handler that dynamically
// builds content from platform resources and module snippets.
func createGettingStartedHandler(
	reg Registry,
	toolReg ToolLister,
	moduleReg *module.Registry,
) ReadHandler {
	return func(ctx context.Context, _ string) (string, error) {
		clientCtx := types.GetClientContext(ctx)

		var sb strings.Builder

		// Write context-specific header.
		switch clientCtx {
		case types.ClientContextCLI:
			sb.WriteString(gettingStartedHeaderCLI)
		default:
			sb.WriteString(gettingStartedHeaderMCP)
		}

		// Module snippets are factual reference — same for all contexts.
		snippets := moduleReg.GettingStartedSnippets()
		if snippets != "" {
			sb.WriteString(snippets)
		}

		// Context-specific tools/commands section.
		switch clientCtx {
		case types.ClientContextCLI:
			writeCLIDiscoverySection(&sb)
		default:
			writeToolsSection(&sb, toolReg)
			writeResourcesSection(&sb, reg)
		}

		// Write context-specific footer.
		switch clientCtx {
		case types.ClientContextCLI:
			sb.WriteString(gettingStartedFooterCLI)
		default:
			sb.WriteString(gettingStartedFooterMCP)
		}

		return sb.String(), nil
	}
}

// writeToolsSection writes the MCP tools listing.
func writeToolsSection(sb *strings.Builder, toolReg ToolLister) {
	sb.WriteString("## Available Tools\n\n")

	tools := toolReg.List()
	sort.Slice(tools, func(i, j int) bool {
		return tools[i].Name < tools[j].Name
	})

	for _, tool := range tools {
		desc := tool.Description
		if idx := strings.Index(desc, "\n"); idx > 0 {
			desc = desc[:idx]
		}

		desc = strings.TrimSpace(desc)

		fmt.Fprintf(sb, "- **%s**: %s\n", tool.Name, desc)
	}
}

// writeResourcesSection writes the MCP resources listing.
func writeResourcesSection(sb *strings.Builder, reg Registry) {
	sb.WriteString("\n## Available Resources\n\n")

	staticResources := reg.ListStatic()
	sort.Slice(staticResources, func(i, j int) bool {
		return staticResources[i].URI < staticResources[j].URI
	})

	for _, res := range staticResources {
		if res.URI == "panda://getting-started" {
			continue
		}

		fmt.Fprintf(sb, "- `%s` - %s\n", res.URI, res.Name)
	}

	templates := reg.ListTemplates()
	if len(templates) > 0 {
		sb.WriteString("\n**Templates:**\n")

		sort.Slice(templates, func(i, j int) bool {
			return templates[i].URITemplate.Raw() < templates[j].URITemplate.Raw()
		})

		for _, tmpl := range templates {
			fmt.Fprintf(sb, "- `%s` - %s\n", tmpl.URITemplate.Raw(), tmpl.Name)
		}
	}
}

// writeCLIDiscoverySection writes CLI discovery instructions.
func writeCLIDiscoverySection(sb *strings.Builder) {
	sb.WriteString("## Discovering Commands and Resources\n\n")
	sb.WriteString("Run `panda --help` to see all available commands.\n")
	sb.WriteString("Run `panda resources` to list available data resources.\n")
	sb.WriteString("Run `panda <command> --help` for details on any command.\n")
}
