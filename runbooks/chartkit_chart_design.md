---
name: Design Chartkit Charts
description: Design-quality layer for chartkit charts in the panda sandbox — which ck.* function fits the question (histogram, bar, line, area, box, scatter, heatmap, waterfall, custom), color jobs (theme presets, value ramps like rainbow/viridis/gradient, stats sentiments good/ok/bad/neutral), multi-series palette order, and the anti-pattern check (dual axis, rainbow on unranked values, number on every point, relative time, duplicated facts). Use when choosing a chart type, picking chart colors, deciding between a plot and a stat tile, or reviewing a chart before saving the PNG.
tags: [chartkit, charts, design, color, visualization]
triggers:
  - which chart type should I use
  - chart colors for multiple series
  - is my chart any good
  - best chart for a latency distribution
  - when to use a value ramp on bars
  - review a chartkit chart before saving
---

Owns the design-quality layer of chartkit charts — form choice, color jobs, and the
anti-pattern check. Use it when about to write any `ck.*` call or before saving a
chart PNG. chartkit output is a static PNG image; embedding it in an interactive
HTML report is fine, but the report's own interaction layer (filters, sortable
tables, theme toggle) and any inline SVG charts it draws by hand are outside this
runbook's scope (see `runbooks://devnet_bug_board_html` for that report's visual
language). The mechanics (which library, saving, sharing, sessions) are owned by
`runbooks://sandbox_output_conventions`; the API contract (two titles, `scope=`,
`source=`, units on axes, no relative time, no duplicated facts) is owned by
chartkit itself — call `ck.guide()` in the sandbox and follow it; this runbook adds
what the guide cannot decide for you.

## Inputs
Query results (a Series or DataFrame) and the question the chart answers. Thin input
is fine: if you only have the question, run the query first — the `title` must state
a finding, and a finding requires having seen the data.

## Output
A saved chartkit PNG that passes the Self-Check below.

## Procedure

1. **Ask "is it even a chart".** A plot is one answer among several:

   | The question's job | Answer |
   |---|---|
   | One headline number ("how many?") | `stats=[...]` tiles on a related chart, or a sentence — not a plot |
   | Shape of one numeric value | `ck.histogram` / `ck.box` |
   | Change over time | `ck.line` / `ck.area` |
   | Compare ranked categories | `ck.bar` |
   | Two measures, correlation? | `ck.scatter` (`trend=True` for the fit + R²) |
   | Density across two dimensions | `ck.heatmap` |
   | Spans / durations / timelines | `ck.waterfall` |
   | None of the above fits | `ck.custom()` — last resort only |

2. **Pick the form by the question, not by habit.** `histogram`/`box` for one
   non-negative numeric column, `bar` for (label, value) pairs, `line` for a
   DataFrame + time column (the window derives from the data), `heatmap` for e.g.
   slot × node arrival density. When unsure, the distribution or the time series is
   usually right.

3. **Label by role, each fact exactly once.** `title` = the finding ("Most blocks
   land inside three seconds"), `chart_title` = the neutral plot label, `subtitle` =
   what is measured + over what, `notes` = caveats only. Never restate one fact in
   two places — chartkit raises on `chart_title == title`; the subtler duplicates it
   cannot catch. No stats → omit `stats`; no caveats → `notes=None`.

4. **Assign color by the job it does — one rule per job:**

   | Color job | Rule in chartkit |
   |---|---|
   | Identity (2–4 series) | Theme colors; per-series color only where exposed (`line` series tuples). Prefer direct labels over color-alone identity |
   | Magnitude / rank | Opt-in value ramp `color="rainbow"\|"viridis"\|"gradient"` on `bar`/`box` — only when the ranking is the story; uniform theme color otherwise |
   | Polarity (above/below a baseline) | No native diverging support: use two charts, a reference line (`hline`/`vline`), or `custom()` |
   | Status | `stats=[...]` sentiments `good\|ok\|bad\|neutral` — judge each number; `neutral` is for pure information (counts, sample size), not a dodge |

   **Never hand-pick hex colors per chart.** The theme presets (default, `warm`,
   `dim`) own the palette — restyle only via `theme=`, never a manual color.

5. **Multi-series rules.** Assign series colors in fixed order, never cycled; more
   than 4 series → fold the tail into "Other" or use small multiples. If a chart
   needs all four network colors, order them `#2f6db0, #cf6a1a, #8e44ad, #1f9b7a`
   (mainnet, holesky, sepolia, hoodi): the palette's coded order renders
   adjacent blue/purple that deuteranopes cannot tell apart (validated ΔE 1.1), and
   this order passes all checks. Direct labels remain the identity backstop.

6. **Run the anti-pattern check** — if the chart matches a row, it is wrong:

   | Anti-pattern | Fix |
   |---|---|
   | A number on every point | `stats=[...]` row + a median marker / one reference line; direct-label ≤ 4 selectively |
   | Relative time ("last 6h", "today") in any label | Absolute UTC window — the PNG outlives the moment (owned by `ck.guide()`) |
   | The same fact in `title`, `subtitle`, and `notes` | Each fact in exactly one place (owned by `ck.guide()`) |
   | Rainbow/value ramp on values with no ranking | Uniform theme color — the ramp claims an order that isn't there |
   | Two measures forced onto one plot | Two charts or small multiples; `line`'s `right=` twin axis only when the series share a domain and the twin scale is the point (e.g. two units of one measure) — never two unrelated measures |
   | More than one reference line plus the axis | One `vline`/`hline` that marks the deadline or median; declutter the rest |
   | A plot where a sentence or stat tile answers the question | `stats=[...]` or prose |
   | Color as the only identity carrier | Legend (≥ 2 series) plus direct labels; one series needs no legend — the title names it |

7. **Render it and look at it.** `.save(path)` (sharing via storage is owned by
   `runbooks://sandbox_output_conventions`), then open the PNG and eyeball it for
   label collisions, truncation, and overflow — the library validates structure, not
   layout. Keep labels Latin (Inter font): ASCII entity/client names, no CJK.

## Self-Check

- `title` states a finding (not the variable name) and differs from `chart_title`.
- `scope=` and `source=` present, with a real source-library object — no invented provenance.
- No relative time anywhere; units live on the axis via `unit=`.
- Stats sentiments judged individually — not everything `good`, not everything `neutral`.
- The PNG has been opened: no collisions, no cut-off text; legend present for ≥ 2 series.
