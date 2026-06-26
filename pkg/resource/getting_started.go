package resource

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/surface"
)

// ToolLister provides access to registered tools.
type ToolLister interface {
	List() []mcp.Tool
}

// sessionWorkflowQuery is the first step of the multi-step session example.
const sessionWorkflowQuery = `# Call 1: Query and SAVE to workspace
df = clickhouse.query("<datasource>", "SELECT ...")
df.to_parquet("/workspace/data.parquet")  # Persist!`

// sessionWorkflowPlot is the second step of the multi-step session example.
const sessionWorkflowPlot = `# Call 2: LOAD from workspace and chart it with chartkit (the default charting library)
import pandas as pd
from ethpandaops import chartkit as ck
from ethpandaops.chartkit.sources.datasources.clickhouse import clickhouse
df = pd.read_parquet("/workspace/data.parquet")  # Load!
ck.line(df, x="time", left=[("Value", "value", "")],
    title="<the finding the chart shows>",      # required: the takeaway
    chart_title="<what the plot is>",           # required: the plot label
    source=clickhouse("<the table/query you read>"),  # required: a source-library object
    scope="<what the data is about, e.g. mainnet>").save("/workspace/chart.png")  # required: never defaulted (use None if global)
uploaded = storage.upload("/workspace/chart.png")
print(uploaded.url)        # public link to share
print(uploaded.host_path)  # where the file lives on the panda server`

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

// createGettingStartedHandler creates a handler that assembles the guide
// from surface-owned onboarding prose and a shared sessions section
// rendered in the surface's dialect.
func createGettingStartedHandler(
	reg Registry,
	toolReg ToolLister,
) ReadHandler {
	return func(_ context.Context, _ string, s surface.Dialect) (string, error) {
		var sb strings.Builder

		sb.WriteString("# Getting Started Guide\n\n")
		sb.WriteString(s.GettingStartedIntro())
		sb.WriteString(s.DiscoveryGuide(discoveryFacts(toolReg, reg)))
		sb.WriteString(sessionsSection(s))

		return sb.String(), nil
	}
}

// discoveryFacts collects the registered tools and resources for the
// surface's discovery guidance.
func discoveryFacts(toolReg ToolLister, reg Registry) surface.Discovery {
	tools := toolReg.List()
	static := reg.ListStatic()
	templates := reg.ListTemplates()

	d := surface.Discovery{
		Tools:     make([]surface.Item, 0, len(tools)),
		Resources: make([]surface.Item, 0, len(static)),
		Templates: make([]surface.Item, 0, len(templates)),
	}

	for _, tool := range tools {
		d.Tools = append(d.Tools, surface.Item{Name: tool.Name, Description: tool.Description})
	}

	for _, res := range static {
		if res.URI == "panda://getting-started" {
			continue
		}

		d.Resources = append(d.Resources, surface.Item{Name: res.URI, Description: res.Name})
	}

	for _, tmpl := range templates {
		d.Templates = append(d.Templates, surface.Item{Name: tmpl.URITemplate.Raw(), Description: tmpl.Name})
	}

	return d
}

// sessionsSection renders the sandbox session semantics, spelling
// invocations in the surface's dialect.
func sessionsSection(s surface.Dialect) string {
	var sb strings.Builder

	sb.WriteString("\n## Sessions\n\n")
	fmt.Fprintf(&sb,
		"**IMPORTANT:** Each %s call runs in a **fresh Python process**. Variables do NOT persist between calls.\n\n",
		s.ExecuteRef())
	sb.WriteString("- **Files persist**: Save to `/workspace/` to share data between executions\n")
	sb.WriteString("- **Variables do NOT persist**: `df` from one call won't exist in the next\n")
	fmt.Fprintf(&sb, "- **Reuse session**: %s\n\n", s.SessionHint())
	sb.WriteString("**Example — Multi-step workflow:**\n")
	sb.WriteString(s.PythonBlock(sessionWorkflowQuery))
	sb.WriteString("\n")
	sb.WriteString(s.PythonBlockInSession(sessionWorkflowPlot))
	fmt.Fprintf(&sb,
		"\nUse `storage.upload()` to publish a file. It returns an `UploadResult` with both "+
			"`.url` (a permanent public link to share) and `.host_path` (where the file lives on the "+
			"panda server) — report both when asked where a file went. Uploads made in one session are "+
			"grouped under that session's directory, so reuse the same session to keep related files "+
			"together (see %s for API details).\n",
		s.PythonDocsRef("storage"))
	fmt.Fprintf(&sb,
		"\n**Charts:** use the `chartkit` library (`from ethpandaops import chartkit as ck`) for any chart "+
			"or plot — histogram, bar, box, line, area, scatter, heatmap, waterfall — not matplotlib/plotly. "+
			"It derives bins/scales/layout and renders a branded PNG; see %s.\n",
		s.PythonDocsRef("chartkit"))

	return sb.String()
}
