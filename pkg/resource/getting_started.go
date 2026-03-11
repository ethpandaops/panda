package resource

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/module"
)

// ToolLister provides access to registered tools.
type ToolLister interface {
	List() []mcp.Tool
}

// gettingStartedHeader contains the static workflow guidance.
const gettingStartedHeader = `# Getting Started Guide

## Workflow

1. **Discover** → Read datasource resources to find available data sources and schemas
2. **Find patterns** → Use the ` + "`search`" + ` tool with the right type for your need:
   - ` + "`search(type=\"examples\", query=\"...\")`" + ` → Query snippets (SQL, PromQL, LogQL)
   - ` + "`search(type=\"runbooks\", query=\"...\")`" + ` → Multi-step investigation procedures
3. **Execute** → ` + "`execute_python`" + ` tool with the ethpandaops library

`

// gettingStartedFooter contains static tips.
const gettingStartedFooter = `
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

// RegisterGettingStartedResources registers the ethpandaops://getting-started
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
			"ethpandaops://getting-started",
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
	return func(_ context.Context, _ string) (string, error) {
		var sb strings.Builder

		// Write header with workflow and critical requirements.
		sb.WriteString(gettingStartedHeader)

		// Include module-specific getting-started snippets.
		snippets := moduleReg.GettingStartedSnippets()
		if snippets != "" {
			sb.WriteString(snippets)
		}

		// Dynamically list tools.
		sb.WriteString("## Available Tools\n\n")

		tools := toolReg.List()
		sort.Slice(tools, func(i, j int) bool {
			return tools[i].Name < tools[j].Name
		})

		for _, tool := range tools {
			// Get first line of description.
			desc := tool.Description
			if idx := strings.Index(desc, "\n"); idx > 0 {
				desc = desc[:idx]
			}

			desc = strings.TrimSpace(desc)

			fmt.Fprintf(&sb, "- **%s**: %s\n", tool.Name, desc)
		}

		// Dynamically list resources.
		sb.WriteString("\n## Available Resources\n\n")

		// Static resources.
		staticResources := reg.ListStatic()
		sort.Slice(staticResources, func(i, j int) bool {
			return staticResources[i].URI < staticResources[j].URI
		})

		for _, res := range staticResources {
			// Skip self-reference.
			if res.URI == "ethpandaops://getting-started" {
				continue
			}

			fmt.Fprintf(&sb, "- `%s` - %s\n", res.URI, res.Name)
		}

		// Template resources.
		templates := reg.ListTemplates()
		if len(templates) > 0 {
			sb.WriteString("\n**Templates:**\n")

			sort.Slice(templates, func(i, j int) bool {
				return templates[i].URITemplate.Raw() < templates[j].URITemplate.Raw()
			})

			for _, tmpl := range templates {
				fmt.Fprintf(&sb, "- `%s` - %s\n", tmpl.URITemplate.Raw(), tmpl.Name)
			}
		}

		// Write footer with tips.
		sb.WriteString(gettingStartedFooter)

		return sb.String(), nil
	}
}
