package surface

import (
	"fmt"
)

// cliResourceCommands maps resource URIs to a dedicated CLI command that
// is preferred over the generic `panda resources <uri>` form.
var cliResourceCommands = map[string]string{
	"datasets://list":      "panda datasets",
	"networks://active":    "panda networks",
	"python://ethpandaops": "panda docs",
}

// cliDialect speaks the CLI dialect: panda commands and flags.
type cliDialect struct{}

var _ Dialect = cliDialect{}

// Key returns the wire identifier for the CLI surface.
func (cliDialect) Key() string { return "cli" }

// ExecuteRef returns the inline reference to the panda execute command.
func (cliDialect) ExecuteRef() string { return "`panda execute`" }

// PythonBlock renders python source wrapped in a panda execute heredoc.
func (cliDialect) PythonBlock(code string) string {
	return "```\npanda execute <<'PY'\n" + code + "\nPY\n```\n"
}

// PythonBlockInSession renders python source for a reused session,
// resuming via the --session flag.
func (cliDialect) PythonBlockInSession(code string) string {
	return "```\npanda execute --session <id> <<'PY'\n" + code + "\nPY\n```\n"
}

// SessionHint says how CLI agents reuse a sandbox session.
func (cliDialect) SessionHint() string {
	return "Pass `--session <id>` for file persistence and faster startup"
}

// SearchExamples returns a panda search invocation scoped to a dataset.
func (cliDialect) SearchExamples(dataset, topic string) string {
	return fmt.Sprintf("`panda search examples --dataset %s %q`", dataset, topic)
}

// ResourceRef returns the CLI command that reads the given resource URI.
func (cliDialect) ResourceRef(uri string) string {
	if cmd, ok := cliResourceCommands[uri]; ok {
		return "`" + cmd + "`"
	}

	return "`panda resources " + uri + "`"
}

// PythonDocsRef returns the panda docs command for the given topic.
func (cliDialect) PythonDocsRef(topic string) string {
	if topic == "" {
		return "`panda docs`"
	}

	return "`panda docs " + topic + "`"
}

// GettingStartedIntro returns the CLI onboarding workflow section.
func (cliDialect) GettingStartedIntro() string {
	return `## Preferred Workflow

Use the narrowest surface that fits the question:

- **One SQL answer**: ` + "`panda clickhouse query-raw <datasource> \"<SQL>\"`" + `
- **One PromQL answer**: ` + "`panda prometheus query <datasource> \"<promql>\"`" + `
- **Protocol constants or spec definitions**: ` + "`panda search consensus-specs \"<topic>\"`" + `
- **Python, plots, files, or cross-source joins**: ` + "`panda execute`" + `

Do not guess command names, table names, columns, or query syntax. Search
examples and runbooks first, then adapt a matching pattern. Search examples
name the target datasource for SQL snippets. Read the dataset guide first when
an example names a dataset.

` + "`panda execute`" + ` is the Python sandbox — the same engine used by MCP clients via
` + "`execute_python`" + `. It provides workspace persistence between calls, multi-step
workflows, and the full ethpandaops library (clickhouse, prometheus, loki,
dora, forky, cbt, ethnode, storage).

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
}

// DiscoveryGuide points CLI readers at the built-in discovery commands;
// the CLI documents its own command set, so the registered tool and
// resource listings are not repeated here.
func (cliDialect) DiscoveryGuide(_ Discovery) string {
	return `## Discovering Data

- ` + "`panda datasets`" + ` — datasets in this deployment and where they live
- ` + "`panda datasets <name>`" + ` — **read before querying a dataset**: required syntax rules and placement
- ` + "`panda datasources`" + ` — connections and the datasets each one holds
- ` + "`panda networks`" + ` — live Cartographoor network/devnet ids; use ` + "`panda networks --devnets`" + ` for active devnets only
- ` + "`panda schema`" + ` — live ClickHouse schemas
- ` + "`panda docs`" + ` — Python module APIs

## Discovering Commands

Run ` + "`panda --help`" + ` to see all available commands.
Run ` + "`panda resources`" + ` to list available data resources.
Run ` + "`panda <command> --help`" + ` for details on any command.

## Finding Examples and Procedures

Before writing a query from scratch, search for prior art:

- ` + "`panda search examples \"<topic>\"`" + ` — query snippets
- ` + "`panda search runbooks \"<topic>\"`" + ` — investigation procedures
- ` + "`panda search eips \"<topic>\"`" + ` — EIP specifications
- ` + "`panda search consensus-specs \"<topic>\"`" + ` — consensus-spec constants and documents
- ` + "`panda search \"<topic>\"`" + ` — search everything at once

Runbooks codify how to debug specific scenarios end-to-end (which datasources to query, which fields to filter on, common pitfalls). For anything non-trivial, start with a runbook search instead of probing raw tables.
`
}
