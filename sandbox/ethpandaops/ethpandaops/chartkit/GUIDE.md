# chartkit — agent usage guide

`chartkit` is the charting library available in the panda sandbox. You call it from
Python; it derives everything visual. You provide **data** and a few **labels**. You
never compute coordinates, ticks, bins, scales, or SVG.

```python
from ethpandaops import chartkit as ck
from ethpandaops.chartkit import sources

src = sources.load("datasources", "prometheus")   # whichever source you actually queried;
                                                   # sources.available() lists what's installed

ck.histogram(values, x="Time into slot", unit="s",
    title="Most blocks land inside three seconds",   # top headline: the finding
    subtitle="First-seen arrival of each block, across all sentries",
    chart_title="Block arrival distribution",        # label on the chart itself (required, ≠ title)
    source=src("the ref you read"),                # required: a source-library object (carries name + logo)
    network="mainnet",                             # required: the network the data is from — never implicit
    stats=[("MEDIAN", "1.46s", "good"), ("SEEN WITHIN 2s", "78%", "good")],
    notes="Excludes blocks with no sentry coverage (<0.1%).",
).save("arrival.png")          # or .url() to upload and get a link
```

`prometheus` above is just whatever you queried — discover the installed set with
`sources.available()`; this guide names no specific source on purpose.

## Nothing is assumed — you state it
The library has **no silent defaults for anything that changes what the chart claims**. You must pass
`network=` explicitly (there is no default — a chart asserts a network in its header, so omitting it
is never allowed), and `source=` must be a real source-library object (a bare string/dict is rejected,
so the footer always carries a verified name + logo). If you leave either out, the call raises with a
message telling you what to add. The point is that you *decide* what lands on the chart.

## The shape of every call
- **Data first** — a Series/array (`histogram`, `bar`) or a DataFrame + column names (`line`, `area`, `heatmap`). Pass the *raw* data; the library bins/aggregates/scales it.
- **`network`** (required) and **`source`** (required) — the provenance. **`title`**, **`chart_title`** (required) and **`subtitle`**, **`unit`**, **`stats`**, **`notes`** (optional) — the labels.
- Returns a `Chart`; call `.save(path)` or `.url()`.

## The two titles — delineated by role
A chart carries **two** titles plus a subtitle. They are not interchangeable:
- **`title`** — *required*. The top headline: the finding/takeaway ("Most blocks land inside three seconds"). Editorial.
- **`subtitle`** — *optional*. The top scope: what is measured + over what ("First-seen arrival, all sentries").
- **`chart_title`** — *required*. The label on the chart itself, centred in the card: the neutral plot name ("Block arrival distribution"). Descriptive, not a restatement of the headline.

Rule of thumb: `title` says *what you found*; `chart_title` says *what the plot is*. They must differ.

## Validated for you (these raise, not render-blank)
The library enforces what it can; a structural mistake fails loudly with a message instead of producing a broken image:
- `title` and `chart_title` present and non-empty · `chart_title` ≠ `title` · `subtitle` ≠ `title`
- `network` is present (no default) and `source`/`sources` carries at least one real source — a chart with no network or no provenance is rejected
- each `stats` entry is `(label, value, sentiment)` with sentiment ∈ `good|ok|bad|neutral`, and there are at most 6 of them
- each `source` is a real source-library object (`sources.load(...)` / a source module) — a bare string or hand-built dict is rejected
- the data you pass isn't empty, is finite (no NaN/inf), and `box` rows carry all five quantiles
- `histogram`/`bar` values are non-negative (bars and bins run from 0; use `custom()` for signed data); a log scale rejects non-positive values rather than dropping them
Everything below is what the library *can't* check — still your responsibility.

## Rules the library can't enforce — follow them

1. **Never duplicate data between fields.** Each fact appears in exactly one place.
   - `title` = the **finding / takeaway** ("Most blocks land inside three seconds"), not the variable name.
   - `subtitle` = **what is measured + scope**, said once ("First-seen arrival across all sentries").
   - `notes` = **caveats only** — exclusions, gaps, interpolation, sampling, "illustrative". If `notes` restates the subtitle, drop it (`notes=None`).
   - ❌ `subtitle="Block arrival"`, `notes="This shows block arrival"` — same data twice.

2. **Units live on the axis, via `unit=`.** Don't bake units into the data or repeat them in labels. `unit="s"` formats ticks as `1s, 2s`; don't also write "(seconds)" in `x`.

3. **Set `stats` sentiments by judgement, not reflex.** Each stat is `(label, value, sentiment)` where sentiment ∈ `good | ok | bad | neutral`. Decide whether the number is actually good. `neutral` is for pure information (counts, sample size) — not a dodge. Don't colour everything `good`.

4. **Omit empty bands.** No stats → don't pass `stats` (the row disappears). No caveats → `notes=None`. No second series → don't force a legend. Don't pad the chart with empty structure.

5. **Attribute what you queried — with a real source library.** `source=` must be a source-library object, never a bare string. Call `sources.available()` to see what's installed, then `src = sources.load("datasources"|"datasets", name); source=src("the ref")`. `ref` is free text (table, metric, join, query) naming what you actually read; the library supplies the verified name + logo so the footer can't carry faked provenance. **Never invent a source you didn't read.** Multiple sources → `sources=[...]`.

6. **Let the library derive the axes, ticks, and window.** Pass raw data and a time column; don't pre-bin, don't compute tick lists, don't hardcode the window — it comes from the data's time range.

7. **Never write relative time — the output is a static image that outlives the moment.** "Last 6 hours", "trailing 24h", "today", "now" all drift: the PNG is saved once and read days later, when they're wrong. Always state the **absolute UTC range** the data actually covers ("21 Jun 14:00 → 20:00 UTC"). For time-series the library derives this `window` from your time column automatically — don't override it with a relative phrase. In `custom()`, compute the window from your data's first/last timestamp and pass it. Same rule for `title`/`subtitle`: say "block arrival, hourly", not "block arrival over the last 6 hours".

8. **`title` states the finding, so look at the data first.** Run the query, see the result, then write the takeaway. "Propagation held flat" only if it did.

9. **Don't over-annotate.** A median marker or a deadline line clarifies; ten labels clutter. Prefer one reference line plus the axis.

10. **Pick the function by the question, not habit.**
   distribution → `histogram`/`box` · over time → `line`/`area` · compare categories → `bar` ·
   density across two dims → `heatmap` · spans/timeline → `timeline`. When unsure, the
   distribution or the time series is usually right.

11. **Theme: leave it default.** The default (light) theme is tuned. For a genuine reason (e.g. embedding in a dark surface) pass a preset: `theme="warm"` or `theme="dim"` (or `theme=ck.WARM`/`ck.DIM`, or a partial theme dict). Don't hand-pick colours per chart; per-series colour overrides exist where they're needed (`line` series tuples, `bar`/`area`/`box` `color=`).

12. **Value gradients are opt-in.** `box`/`bar` default to one brand colour. When the value itself is the story (a clear ranking/ramp), pass `color="rainbow"` (or `"viridis"`/`"gradient"`) to colour each mark by its value, with a matching gradient legend. Use it when magnitude order carries meaning; leave the uniform default otherwise.

13. **Keep labels to Latin text.** Layout is measured from the chart font (Inter). Latin/Greek/Cyrillic and emoji render best-effort via font fallback; CJK and unusual scripts may not have glyphs and their spacing isn't measured — prefer ASCII names (which is what Ethereum entity/client/table names already are).

## When the library doesn't have your chart
Most needs are covered. For something genuinely custom (an unusual mark, a second axis
the high-level call doesn't expose), there's a lower-level escape hatch — but reach for it
only when no `ck.*` function fits, and keep the same rules above.

## Logos for custom panels
Ethereum client logos are available via the `clients` library — `clients.CLIENTS` lists the
installed set, `clients.logo(name)` returns a data-uri you can place in a custom draw/slot.
Like sources, the list is discovered, never assumed — don't hardcode client names.
