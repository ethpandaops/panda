package resource

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/types"
)

// ToolLister provides access to registered tools.
type ToolLister interface {
	List() []mcp.Tool
}

// gettingStartedHeaderMCP contains the MCP workflow guidance. It teaches the
// system only: no dataset, datasource, table, or network is ever named here —
// those facts live behind the resources this guide points at.
const gettingStartedHeaderMCP = `# Getting Started Guide

## Workflow

1. **Discover** → ` + "`datasets://list`" + ` shows the datasets this deployment holds; ` + "`datasources://list`" + ` shows the connections that hold them (placement params + operator notes); ` + "`networks://active`" + ` shows live network/devnet ids
2. **Learn the data** → **read ` + "`datasets://{name}`" + ` before querying a dataset** — its required syntax rules and placement live there. Live schema: ` + "`clickhouse://tables/{cluster}/{database}`" + `
3. **Find patterns** → Use the ` + "`search`" + ` tool to find relevant examples and procedures:
   - ` + "`search(query=\"...\")`" + ` → Search everything (examples, runbooks, EIPs, consensus specs)
   - ` + "`search(type=\"examples\", query=\"...\", dataset=\"...\")`" + ` → Query snippets, optionally scoped to one dataset
   - ` + "`search(type=\"runbooks\", query=\"...\")`" + ` → Investigation procedures only
   - ` + "`search(type=\"consensus-specs\", query=\"...\")`" + ` → Consensus-specs documents and protocol constants
4. **Execute** → ` + "`execute_python`" + ` tool with the ethpandaops library; Python module APIs: ` + "`python://ethpandaops`" + `

`

// gettingStartedHeaderCLI contains the CLI workflow guidance.
const gettingStartedHeaderCLI = `# Getting Started Guide

## Preferred Workflow

Use the narrowest surface that fits the question:

- **One SQL answer**: ` + "`panda clickhouse query-raw <datasource> \"<SQL>\"`" + `
- **One PromQL answer**: ` + "`panda prometheus query <datasource> \"<promql>\"`" + `
- **Python, plots, files, or cross-source joins**: ` + "`panda execute`" + `

Search examples name the target datasource for SQL snippets. Read the dataset
guide first when an example names a dataset.

` + "`panda execute`" + ` is the Python sandbox — the same engine used by MCP clients via
` + "`execute_python`" + `. It provides workspace persistence between calls, multi-step
workflows, and the full ethpandaops library (clickhouse, prometheus, loki,
dora, ethnode, storage).

### Quick Start

` + "```" + `
panda datasets
panda search examples "<topic>"
panda clickhouse query-raw <Target> "<SQL from the example, adjusted for the question>"
` + "```" + `

For Python workflows:

` + "```" + `
panda execute <<'PY'
from ethpandaops import clickhouse
df = clickhouse.query("<datasource>", "SELECT ...")
print(df)
PY
` + "```" + `

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
df = clickhouse.query("<datasource>", "SELECT ...")
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
panda execute <<'PY'
df = clickhouse.query("<datasource>", "SELECT ...")
df.to_parquet("/workspace/data.parquet")
PY
` + "```" + `

` + "```" + `
panda execute --session <id> <<'PY'
import pandas as pd
df = pd.read_parquet("/workspace/data.parquet")
plt.plot(df["time"], df["value"])
plt.savefig("/workspace/chart.png")
url = storage.upload("/workspace/chart.png")
print(url)
PY
` + "```" + `

Use ` + "`storage.upload()`" + ` for permanent public URLs (see ` + "`panda docs storage`" + ` for API details).
`

// RegisterGettingStartedResources registers the panda://getting-started
// resource.
func RegisterGettingStartedResources(
	log logrus.FieldLogger,
	reg Registry,
	toolReg ToolLister,
) {
	log = log.WithField("resource", "getting_started")

	reg.RegisterStatic(StaticResource{
		Resource: mcp.NewResource(
			"panda://getting-started",
			"Getting Started Guide",
			mcp.WithResourceDescription("Essential guide for querying data - read this first!"),
			mcp.WithMIMEType("text/markdown"),
			mcp.WithAnnotations([]mcp.Role{mcp.RoleAssistant}, 1.0, ""),
		),
		Handler: createGettingStartedHandler(reg, toolReg),
	})

	log.Debug("Registered getting-started resource")
}

// createGettingStartedHandler creates a handler that dynamically
// builds content from platform resources and module snippets.
func createGettingStartedHandler(
	reg Registry,
	toolReg ToolLister,
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
	sb.WriteString("## Discovering Data\n\n")
	sb.WriteString("- `panda datasets` — datasets in this deployment and where they live\n")
	sb.WriteString("- `panda datasets <name>` — **read before querying a dataset**: required syntax rules and placement\n")
	sb.WriteString("- `panda datasources` — connections and the datasets each one holds\n")
	sb.WriteString("- `panda resources read networks://active` — live network/devnet ids; use the id, not the display name, when reading `networks://<id>`\n")
	sb.WriteString("- `panda schema` — live ClickHouse schemas\n")
	sb.WriteString("- `panda docs` — Python module APIs\n")
	sb.WriteString("\n## Discovering Commands\n\n")
	sb.WriteString("Run `panda --help` to see all available commands.\n")
	sb.WriteString("Run `panda resources` to list available data resources.\n")
	sb.WriteString("Run `panda <command> --help` for details on any command.\n")
	sb.WriteString("\n## Finding Examples and Procedures\n\n")
	sb.WriteString("Before writing a query from scratch, search for prior art:\n\n")
	sb.WriteString("- `panda search examples \"<topic>\"` — query snippets\n")
	sb.WriteString("- `panda search runbooks \"<topic>\"` — investigation procedures\n")
	sb.WriteString("- `panda search eips \"<topic>\"` — EIP specifications\n")
	sb.WriteString("- `panda search consensus-specs \"<topic>\"` — consensus-spec constants and documents\n")
	sb.WriteString("- `panda search \"<topic>\"` — search everything at once\n")
	sb.WriteString("\nRunbooks codify how to debug specific scenarios end-to-end ")
	sb.WriteString("(which datasources to query, which fields to filter on, common pitfalls). ")
	sb.WriteString("For anything non-trivial, start with a runbook search instead of probing raw tables.\n")
}
