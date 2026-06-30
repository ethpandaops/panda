// Package chartkit surfaces the chartkit sandbox charting library to agents.
//
// chartkit is pure sandbox Python (it lives in the ethpandaops package and
// renders SVG charts via librsvg), with no datasource or proxy dependency. This
// module is therefore docs-only: it contributes Python API docs and examples so
// agents can discover and use the library through execute_python and search.
package chartkit

import (
	"context"

	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/types"
)

// Compile-time interface checks.
var (
	_ module.Module                = (*Module)(nil)
	_ module.DefaultEnabled        = (*Module)(nil)
	_ module.ExamplesProvider      = (*Module)(nil)
	_ module.PythonAPIDocsProvider = (*Module)(nil)
)

// Module implements the module.Module interface for chartkit (docs-only).
type Module struct{}

// New creates a new chartkit module.
func New() *Module { return &Module{} }

func (m *Module) Name() string { return "chartkit" }

// DefaultEnabled keeps the docs available without any datasource or config.
func (m *Module) DefaultEnabled() bool { return true }

func (m *Module) Init(_ []byte) error           { return nil }
func (m *Module) ApplyDefaults()                {}
func (m *Module) Validate() error               { return nil }
func (m *Module) Start(_ context.Context) error { return nil }
func (m *Module) Stop(_ context.Context) error  { return nil }

// Examples contributes charting examples to the semantic search index.
func (m *Module) Examples() map[string]types.ExampleCategory {
	return map[string]types.ExampleCategory{
		"chartkit": {
			Name:        "Charting (chartkit)",
			Description: "Render publication-quality PNG charts from query results with the chartkit sandbox library",
			Examples: []types.Example{
				{
					Name:        "Histogram of block arrival",
					Description: "Labelled distribution chart from a numeric Series, saved as a PNG",
					Query: `from ethpandaops import chartkit as ck
from ethpandaops.chartkit.sources.datasets.xatu import xatu

ck.histogram(df["arrival_s"], x="Time into slot", unit="s",
    title="Most blocks land inside three seconds",   # the finding (top headline)
    chart_title="Block arrival distribution",        # the plot label (on the chart)
    source=xatu("mainnet.fct_block_first_seen_by_node"),  # required: a source-library object
    scope="mainnet",                                 # required: what the chart is about (usually the network)
).save("arrival.png")`,
				},
				{
					Name:        "Bar chart of a per-entity value",
					Description: "Horizontal bar chart from (label, value) pairs",
					Query: `from ethpandaops import chartkit as ck
from ethpandaops.chartkit.sources.datasets.xatu import xatu

ck.bar([(name, count) for name, count in rows], value_label="Blocks proposed",
    title="Binance proposes the most blocks",
    chart_title="Blocks proposed by entity",
    source=xatu("mainnet.fct_block_proposer_entity"),  # required: a source-library object
    scope="mainnet",                                   # required: what the chart is about (usually the network)
).save("entities.png")`,
				},
				{
					Name:        "Time series with restart events",
					Description: "Overlay process/container restarts on a Prometheus line chart with markers=ck.events()",
					Query: `from ethpandaops import chartkit as ck, prometheus
from ethpandaops.chartkit.sources.datasources.prometheus import prometheus as src

# capture restarts (reads process_start_time_seconds; its value IS the start time)
restarts = prometheus.restarts("devnet", match='job=~"ethrex.*"',
    start="2026-06-29T15:00:00Z", end="2026-06-30T13:00:00Z")  # -> [{t, label, kind, series}, ...]

ck.line(df, x="time", left=[("prysm-ethrex-1", "slots_behind", "slots")],
    markers=ck.events(restarts, style="dot"),    # a dot on the series at each restart (style="rule" for vertical lines)
    title="Prysm stalled until the EL was restarted",
    chart_title="Slots behind chain head",
    source=src("beacon_head_slot"),              # required: a source-library object
    scope="devnet",                              # required: what the chart is about
).save("restarts.png")`,
				},
			},
		},
	}
}

// PythonAPIDocs documents the chartkit public functions for `panda docs chartkit`.
func (m *Module) PythonAPIDocs() map[string]types.ModuleDoc {
	return map[string]types.ModuleDoc{
		"chartkit": {
			Description: "Browser-free chart rendering for the sandbox. Pass data + plain labels; chartkit derives bins, domains, ticks, scales, layout and SVG, then writes a PNG via librsvg. Every chart REQUIRES `title` (the finding), `chart_title` (the plot label), `scope=` (what the chart is about — usually the network like `scope=\"mainnet\"`, but any short label for non-network data such as a hardware platform; never defaulted, so a chart can't silently mislabel; pass `scope=None` only for genuinely global data), and `source=` that is a source-library object (e.g. `xatu(...)`, `clickhouse(...)`, `prometheus(...)`, `tempo(...)`, `postgres(...)`) carrying a verified name + logo — bare strings and hand-built dicts are rejected. At most 6 `stats`. Restyle with `theme=\"warm\"|\"dim\"`; override per-series colours via the `line` series tuples or `color=`. Discover sources with `from ethpandaops.chartkit import sources; sources.available()`, client logos with `clients.CLIENTS`. Read the full rules with `ck.guide()`.",
			Functions: map[string]types.FunctionDoc{
				"histogram": {Signature: `histogram(values, *, x, unit="", title, chart_title, source, scope, subtitle="", stats=None, notes="", median=True, bins=80) -> Chart`, Description: "Distribution of a 1-D non-negative numeric Series/array. source= and scope= are required"},
				"bar":       {Signature: `bar(items, *, value_label="", unit="", title, chart_title, source, scope, sort=True, color="data") -> Chart`, Description: "Horizontal bars for named categories; items are (label, value) pairs. color= can be a colour or a value ramp (\"rainbow\"/\"viridis\"/\"gradient\") to colour bars by value. source= and scope= are required"},
				"box":       {Signature: `box(rows, *, x_label, x_unit="", title, chart_title, source, scope, sort="med", color="data") -> Chart`, Description: "Box plot; rows are dicts with label,p05,q1,med,q3,p95. color= can be a value ramp (\"rainbow\"/\"viridis\"/\"gradient\") to colour boxes by value. source= and scope= are required"},
				"line":      {Signature: `line(df, *, x, left, right=None, y_scale="linear", y_max=None, markers=None, title, chart_title, source, scope) -> Chart`, Description: "Line chart; left/right series are (label, column, unit[, color]); right= adds a second axis; datetime x auto-derives the window. source= and scope= are required"},
				"area":      {Signature: `area(df, *, x, y, unit="", y_label=None, color=GREEN, title, chart_title, source, scope) -> Chart`, Description: "Filled single time/numeric series. source= and scope= are required"},
				"scatter":   {Signature: `scatter(df, *, x, y, x_label=None, y_label=None, x_scale="linear", y_scale="linear", trend=False, title, chart_title, source, scope) -> Chart`, Description: "Scatter plot with optional least-squares trend line and R². A log scale rejects non-positive values. source= and scope= are required"},
				"heatmap":   {Signature: `heatmap(cells, *, x_labels, y_labels, x_title="", lo="", hi="", x_step=None, title, chart_title, source, scope) -> Chart`, Description: "2-D density; cells are (col_index, row_index, value); row 0 is the bottom row. x_step=None auto-thins x labels. source= and scope= are required"},
				"waterfall": {Signature: `waterfall(spans, *, x_label="", title, chart_title, source, scope) -> Chart`, Description: "Span timeline (Jaeger-style); spans are dicts with name, start, dur (ms)[, color]. source= and scope= are required"},
				"custom":    {Signature: `custom(*, draw, xdom, ydom, xticks, yticks, x_label="", y_label="", title, chart_title, source, scope) -> Chart`, Description: "Escape hatch: draw bespoke marks via the Draw context (c.line/c.rect/c.dot/...) when no built-in type fits. source= and scope= are required"},
				"hline":     {Signature: `hline(value, label="", color="deadline") -> dict`, Description: "A horizontal reference line for markers=[...]"},
				"vline":     {Signature: `vline(value, label="", color="deadline", dash=True) -> dict`, Description: "A vertical reference line for markers=[...]"},
				"events":    {Signature: `events(records, *, style="rule", kind=None, color=None) -> list[dict]`, Description: "Turn timestamped events into markers=[...] for line/area. Each record is {t (unix seconds or datetime), label, kind (restart/deploy/fork/outage/info -> colour), series}. style=\"rule\" draws a labelled vertical line per event; style=\"dot\" drops a dot on the matching series (cheaper when events are frequent or per-series). Capture restarts with prometheus.restarts(); pass real timestamps (a time-series chart maps them onto its x-axis)."},
				"guide":     {Signature: `guide() -> str`, Description: "The full chartkit usage rules an agent must follow (titles, units, attribution, no relative time, ...)"},
			},
		},
	}
}
