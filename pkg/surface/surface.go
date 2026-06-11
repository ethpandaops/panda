// Package surface defines how each client surface (MCP, CLI) addresses
// panda's capabilities. Content renderers receive a Dialect and use it to
// spell invocations and onboarding guidance in the reader's dialect; they
// never branch on caller identity themselves. Handlers own facts and
// structure, surfaces own voice and addressing.
package surface

// QueryParam is the HTTP query parameter that selects a surface on the
// product API. The value is a Dialect key.
const QueryParam = "client_context"

// Dialect renders invocation references and onboarding prose for one
// client surface. Implementations must be stateless values.
type Dialect interface {
	// Key returns the wire identifier for this surface ("mcp", "cli").
	Key() string

	// ExecuteRef returns an inline reference to the Python execution
	// entrypoint, e.g. "`execute_python`" or "`panda execute`".
	ExecuteRef() string

	// PythonBlock renders python source as a runnable fenced block in
	// this surface's dialect.
	PythonBlock(code string) string

	// PythonBlockInSession renders python source as a runnable fenced
	// block that resumes an existing sandbox session.
	PythonBlockInSession(code string) string

	// SessionHint says how a reader on this surface reuses a sandbox
	// session between executions.
	SessionHint() string

	// SearchExamples returns an inline examples-search invocation scoped
	// to a dataset.
	SearchExamples(dataset, topic string) string

	// ResourceRef returns an inline reference for reading the given
	// resource URI on this surface.
	ResourceRef(uri string) string

	// PythonDocsRef returns a reference to the Python API docs for the
	// given topic.
	PythonDocsRef(topic string) string

	// GettingStartedIntro returns the surface's onboarding workflow
	// section for the getting-started guide.
	GettingStartedIntro() string

	// DiscoveryGuide renders how readers on this surface discover
	// capabilities, given the currently registered tools and resources.
	DiscoveryGuide(d Discovery) string
}

// Discovery carries the facts a surface may use to render its capability
// discovery guidance.
type Discovery struct {
	Tools     []Item
	Resources []Item
	Templates []Item
}

// Item is a name plus a one-line description.
type Item struct {
	Name        string
	Description string
}

// MCP is the surface for MCP clients (LLM tool use). It is the default.
var MCP Dialect = mcpDialect{}

// CLI is the surface for agents driving the panda CLI.
var CLI Dialect = cliDialect{}

// FromKey resolves a wire identifier to a Dialect. Unknown or empty keys
// resolve to MCP, the default surface.
func FromKey(key string) Dialect {
	if key == CLI.Key() {
		return CLI
	}

	return MCP
}
