---
name: Devnet Consensus Bug Report
description: Scan a devnet for consensus health issues (missed blocks, orphaned blocks, reorgs, participation drops, splits) over a time window, cluster them into a ranked summary, launch one investigation agent per confirmed issue, and publish a single self-contained interactive HTML bug board cross-referenced to ethpandaops tooling — Dora slots/epochs, instance/ssh pages, GitHub PRs and source lines, EIP-at-commit, Kurtosis config — with a per-bug overview, root cause, discovery timeline, inline charts, embedded panda reproduction scripts, and fix status. Use for periodic devnet status reports, incident roundups, and building a CL/EL client bug knowledge base.
tags: [devnet, consensus, bug-report, leaderboard, html, orchestration]
triggers:
  - generate a devnet consensus bug report
  - scan the devnet for missed blocks reorgs splits and build a bug board
  - html bug leaderboard ranking client issues with upvotes
  - periodic devnet status report or incident roundup
  - build a CL EL client bug knowledge base
prerequisites: [clickhouse-raw, dora]
---

This runbook produces a devnet consensus **bug board**: a single self-contained HTML page that scans a network for consensus issues, ranks them, and renders each as a richly cross-referenced bug entry wired into ethpandaops tooling. It runs in four phases — scan, summarize, investigate, report.

It is an orchestrator. It does not re-implement per-issue investigation: each spawned agent runs `runbooks://debug_ethereum_network` against the report's `network_target` (hosted devnet or local Kurtosis enclave), scoped to one issue window, and returns one structured bug object. Read `runbooks://debug_ethereum_network` before starting so the Phase 3 hand-off makes sense.

## What This Produces

One HTML file with two layers:

1. **Leaderboard index** — every bug is a row, ranked by upvotes (default), with client-side text search, clickable **severity filter chips**, client and has-PR/no-PR filters, and click-to-sort columns. This is the "bug ranking page" surface. Each row carries an **upvote button** the viewer can toggle; their own vote persists in `localStorage` and re-ranks the board live.
2. **Per-bug detail** — for each bug: status/severity badges, an upvote button, a connections panel (network, instances, docker images, client flags, PRs + state, EIP-at-commit, source lines, Kurtosis config, labels), an overview, a root cause, a discovery timeline with event markers, inline time-series charts, an embedded panda reproduction script, and a fix section.

The visual language follows the EthPandaOps bug-leaderboard product: light theme by default (indigo accent, JetBrains-Mono-first code, soft tinted status/severity chips), with a dark variant available via the "◐ theme" toggle, persisted in `localStorage`. It intentionally does not auto-switch to dark on the OS preference — the product look is the default so the page always opens in the light-indigo style. Architecturally it mirrors the repo's `tests/eval/scripts/report_template.html`: CSS design tokens, zero external fetches, safe to serve as an uploaded asset.

It is a **static snapshot**. The viewer's own upvote is saved in their browser, but **shared vote totals, comments, and live ClickHouse log/chart streaming need the bug-board backend and are out of scope for the static asset** — the page bakes a snapshot and links out to live tooling. Say so plainly in the delivered report; do not imply the static file is live or that upvotes are shared.

## Inputs

- **Network id** — required. Resolve it into a `network_target` with `runbooks://hosted_devnet_context` (`networks://active`, `dora.list_networks()`, `ethnode.list_networks()`); for a local enclave use `runbooks://kurtosis_devnet`. Do not guess.
- **Time window** — the reporting period. Use the user's window verbatim if given; otherwise default to the past 4 epochs and state the default. Pin one concrete slot/epoch range and reuse it across every scan query; record it in the report header.

## Workflow

1. **Scan** — collect consensus issue candidates over the window.
2. **Summarize** — cluster candidates into bugs, rank by severity, present the summary, and prompt the user for which to investigate.
3. **Investigate** — spawn one agent per confirmed bug; each returns one bug object.
4. **Report** — render all bug objects into one HTML bug board and publish it.

## Phase 1: Scan For Consensus Issues

First establish a baseline (split? finalizing? participating?) by building the protocol model with `runbooks://ethereum_protocol_model`, as `runbooks://debug_ethereum_network` does in its procedure step 2. A report on a healthy network is a valid outcome — say so.

Then collect issue candidates over the window using Panda examples; do not hardcode Dora/Forky/ClickHouse queries from memory.

| Candidate class | What to collect | Example query |
| --- | --- | --- |
| Missed blocks | `status=Missing` slots, scheduled proposer, node/client | `search(type="examples", query="missed slots over a time range")` |
| Orphaned blocks | blocks produced but non-canonical (reorged out), orphan count per epoch | `search(type="examples", query="orphaned blocks and reorgs")` |
| Reorgs / splits | competing head roots, fork-choice divergence, depth | `search(type="examples", query="network splits fork choice")` |
| Participation drops | per-epoch target/head participation below threshold | `search(type="examples", query="attestation participation by epoch")` |
| Client/EL validation errors | `INVALID` payloads, gasUsed/receiptsRoot/BAL mismatches in logs | `search(type="examples", query="Recent node errors")` |

For every candidate record the class, exact slot/epoch(s), scheduled proposer or affected block root, implicated node/client, and the query that produced it. Keep the raw rows — they become citations and timeline events. Scanning rules: judge participation on completed epochs only (`runbooks://ethereum_protocol_model`); a split is itself top severity; record source disagreements rather than silently picking one (`runbooks://evidence_discipline`).

## Phase 2: Cluster And Summarize

Cluster candidates into bugs before reporting so one root cause is not counted fifty times. Group missed/orphaned slots sharing a proposer node, client type, validator range, or contiguous slot run into one bug. Rank each with the rubric below, assign stable ids (`MISS-01`, `ORPH-01`, `SPLIT-01`, `PART-01`, `EL-01`), and emit a summary table: `id | class | severity | window | affected | count | one-line`.

Then **prompt the user**: present the summary and baseline, and ask which bugs to investigate (default: everything `major` and above). Do not fan out agents until the user confirms — investigation is the expensive step.

### Severity Rubric

| Severity | Signal |
| --- | --- |
| `critical` | network split, finality stalled >8 epochs, or one client fully off the canonical chain |
| `major` | participation below 66.7%, finality lag 4–8 epochs, a miss/orphan streak concentrated on one client type |
| `minor` | isolated single-node misses, orphan count in normal churn range, errors with no chain symptom |

## Phase 3: Per-Bug Investigation

For each confirmed bug, launch one dedicated investigation agent. Agents run concurrently and independently; send them in a single batch.

Each agent MUST:

1. Run `runbooks://debug_ethereum_network` **scoped to this one bug's window and symptom class only**, picking the matching row from its symptom → branch table (missed → missed beacon blocks, split → network split / fork-choice, EL mismatch → EL / engine API, …).
2. Obey every `runbooks://evidence_discipline` rule on citations, verbatim output, and data-quality caveats.
3. Populate the bug schema from evidence. **Omit any field the evidence does not support — never fabricate a PR, image, flag, source line, or EIP commit.** Return exactly one bug object.

Hand-off contract — give each agent the network id, the frozen window, the bug id/class, the candidate rows already collected, the Cross-Reference Vocabulary below, and this schema:

Placeholders in `<...>` are illustrative — fill them from evidence, and drop any field you cannot support.

```yaml
id: <ID>                           # stable id from the summary, e.g. EL-01 / MISS-02
title: "<one-line bug title>"
subtitle: "<one-line mechanism>"
severity: <critical|major|minor>
upvotes: 0                         # baked-in snapshot count; humans/agents seed it
first_seen: "<YYYY-MM-DD>"
status_badges:                     # pills; kind ∈ open|confirmed|investigating|draft|fixed
  - { text: "<label>", kind: <kind> }
labels: ["<free-form label>"]                      # e.g. "missing from EELS testing"; chips + filterable
connections:                       # all optional — include only what the evidence supports
  clients: ["<el> EL", "<cl> CL"]                  # drives leaderboard client filter
  instances: ["<instance-name>"]                   # → ethpandaops.io instance page + ssh line
  images:  [{ role: "<CL|EL>", image: "<image:tag@digest>" }]      # one or more CL/EL images
  flags:   [{ client: "<client>", flags: "<--flag value>" }]
  prs:     [{ repo: "<org/repo>", number: <n>, state: <open|merged|closed|draft> }]  # one or many
  eip:     { number: <n>, commit: "<eip-commit-sha>" }             # EIP pinned to its commit version
  source_refs: [{ repo: "<org/repo>", ref: "<branch-or-sha>",      # any repo: client, EELS/execution-specs, consensus-specs
                  path: "<path/to/file>", line: <n>, line_end: <n> }]
  kurtosis_config_url: "https://github.com/ethpandaops/<network>/blob/<ref>/network-params.yaml"
overview_html:   "<p>Plain-language summary with <code>inline code</code>.</p>"
root_cause_html: "<p>Uses the class vocabulary: <span class=\"delta\">-180731</span>, <table class=\"fork-table\">…</table>, <pre>…</pre></p>"
timeline:                          # kind ∈ restart|block|timing|log|note → coloured markers
  - { ts: "<YYYY-MM-DD HH:MMZ>", kind: <kind>, slot: <n>,
      text: "<what happened>",
      log: "<verbatim log line, HTML-escaped by the generator>" }
series:                            # rendered as inline SVG; events draw vertical markers
  - { title: "<metric>", unit: "<unit>", points: [[<x>,<y>], ...],
      events: [{ x: <x>, label: "<event>" }] }
repro:                             # embedded panda python the reader runs to reproduce
  - { title: "<what this proves>", code: "from ethpandaops import ethnode\n..." }
fix_html:  "<div class=\"note\">Fix PR applies the change in the missing path — <status>.</div>"
citations: ["panda resources read networks://<network>", "panda execute --code '...'"]
```

`overview_html`, `root_cause_html`, and `fix_html` are agent-composed HTML restricted to the class vocabulary in Phase 4 (`code`, `pre`, `delta`, `fork-table` with `.canonical`/`.divergent`, `note`). HTML-escape log lines. If the investigation only narrows the bug to a class, say so — do not overstate certainty.

## Cross-Reference Vocabulary

Every concrete artifact links to live ethpandaops tooling. **Prefer service URLs from `networks://<network>`** (Dora, Forky, tracoor, beacon/JSON-RPC endpoints, `repository`, `node_inventory_url`); fall back to the conventional patterns below only when the resource omits them.

| Target | URL pattern | Display |
| --- | --- | --- |
| Dora slot | `https://dora.<net>.ethpandaops.io/slot/<n>` | `Slot <n>` |
| Dora epoch / validator / block | `…/epoch|validator|block/<id>` | `Epoch <n>` etc. |
| Instance | `https://ethpandaops.io/networks/<net>/?tab=instances&instance=<name>` | `ssh devops@<name>.srv.<net>.ethpandaops.io` |
| GitHub PR | `https://github.com/<repo>/pull/<n>` | `<repo>#<n>` + state badge |
| GitHub commit | `https://github.com/<repo>/commit/<sha>` | `<sha[:9]>` |
| Source line(s) | `https://github.com/<repo>/blob/<ref>/<path>#L<a>-L<b>` | `<file>:<a>` |
| EIP at commit | `https://github.com/ethereum/EIPs/blob/<commit>/EIPS/eip-<nnnn>.md` | `EIP-<n> @ <commit[:7]>` |
| Kurtosis config | dump a config.yaml that can be used to reproduce the bug into the html document if you've verified the reproduction is possible |
The Phase 4 generator implements each as a small `link_*` helper so agents supply structured fields, not raw URLs.

## Phase 4: Build And Publish The Bug Board

Collect every bug object into `/workspace/bugs.json`, then render and publish. The generator owns page chrome, CSS tokens, the leaderboard, the link vocabulary, and all structured rendering; agents supply only the per-bug `*_html` narrative.

```python
import html, json
from datetime import datetime
from ethpandaops import storage

NETWORK = "<network>"
WINDOW  = "<slot/epoch range>"
# Prefer the Dora service URL from networks://<network>; fall back to the convention.
DORA_BASE = f"https://dora.{NETWORK}.ethpandaops.io"
bugs = json.load(open("/workspace/bugs.json"))

def esc(s): return html.escape("" if s is None else str(s))

# ---- cross-reference link vocabulary ----
def a(url, text, ext=True):
    if not url: return esc(text)
    rel = ' target="_blank" rel="noopener"' if ext else ''
    return f'<a href="{esc(url)}"{rel}>{esc(text)}</a>'
def dora_slot(n):  return a(f"{DORA_BASE}/slot/{n}", f"Slot {n}")
def instance(name):
    url = f"https://ethpandaops.io/networks/{NETWORK}/?tab=instances&instance={name}"
    return a(url, f"ssh devops@{name}.srv.{NETWORK}.ethpandaops.io")
def gh_pr(repo, n):     return a(f"https://github.com/{repo}/pull/{n}", f"{repo}#{n}")
def gh_line(ref):
    frag = f"#L{ref['line']}" + (f"-L{ref['line_end']}" if ref.get('line_end') else "")
    return a(f"https://github.com/{ref['repo']}/blob/{ref['ref']}/{ref['path']}{frag}",
             f"{ref['path'].split('/')[-1]}:{ref['line']}")
def eip_at(n, commit):
    return a(f"https://github.com/ethereum/EIPs/blob/{commit}/EIPS/eip-{n:04d}.md", f"EIP-{n} @ {esc(commit[:7])}")

# ---- render helpers ----
def badge(text, kind): return f'<span class="badge b-{esc(kind)}">{esc(text)}</span>'
PR_KIND = {"merged":"fixed","open":"open","draft":"draft","closed":"minor"}

def connections(b):
    c, rows = b.get("connections", {}), []
    def r(k, v):
        if v: rows.append(f'<span class="k">{esc(k)}</span><span class="v">{v}</span>')
    r("Network", a(f"https://ethpandaops.io/networks/{NETWORK}/", NETWORK))
    if c.get("instances"): r("Instances", " · ".join(instance(n) for n in c["instances"]))
    if c.get("images"):    r("Images", "<br>".join(f'{esc(i["role"])}: <code>{esc(i["image"])}</code>' for i in c["images"]))
    if c.get("flags"):     r("Flags", "<br>".join(f'{esc(f["client"])}: <code>{esc(f["flags"])}</code>' for f in c["flags"]))
    if c.get("prs"):       r("Pull requests", " · ".join(gh_pr(p["repo"], p["number"]) + " " + badge(p["state"], PR_KIND.get(p["state"],"minor")) for p in c["prs"]))
    if c.get("eip"):       r("EIP", eip_at(c["eip"]["number"], c["eip"]["commit"]))
    if c.get("source_refs"): r("Source", " · ".join(gh_line(x) for x in c["source_refs"]))
    if c.get("kurtosis_config_url"): r("Kurtosis config", a(c["kurtosis_config_url"], "ethereum-package config"))
    if b.get("labels"):    r("Labels", " ".join(badge(l, "label") for l in b["labels"]))
    return f'<div class="kv">{"".join(rows)}</div>' if rows else ""

def timeline(events):
    if not events: return ""
    items = []
    for e in events:
        ref = " " + dora_slot(e["slot"]) if e.get("slot") is not None else ""
        pre = f'<pre>{esc(e["log"])}</pre>' if e.get("log") else ""
        items.append(f'<div class="tl-item tl-{esc(e.get("kind","note"))}">'
                     f'<div class="tl-date">{esc(e.get("ts",""))} · {esc(e.get("kind","note"))}{ref}</div>'
                     f'<div>{e.get("text","")}</div>{pre}</div>')
    return f'<div class="timeline">{"".join(items)}</div>'

def sparkline(s):
    pts = s.get("points", [])
    if len(pts) < 2: return ""
    W, H, pad = 720, 90, 6
    xs, ys = [p[0] for p in pts], [p[1] for p in pts]
    x0, x1, y0, y1 = min(xs), max(xs), min(ys), max(ys)
    dx, dy = (x1 - x0) or 1, (y1 - y0) or 1
    X = lambda x: pad + (x - x0) / dx * (W - 2*pad)
    Y = lambda y: H - pad - (y - y0) / dy * (H - 2*pad)
    poly = " ".join(f"{X(x):.1f},{Y(y):.1f}" for x, y in pts)
    marks = "".join(f'<line x1="{X(ev["x"]):.1f}" y1="0" x2="{X(ev["x"]):.1f}" y2="{H}" class="ev"><title>{esc(ev.get("label",""))}</title></line>' for ev in s.get("events", []))
    return (f'<figure class="chart"><figcaption>{esc(s.get("title",""))} '
            f'<span class="mut">({esc(s.get("unit",""))}) · {y0:g}–{y1:g}</span></figcaption>'
            f'<svg viewBox="0 0 {W} {H}" preserveAspectRatio="none">{marks}'
            f'<polyline points="{poly}" class="spark"/></svg></figure>')

def repro(b):
    blocks = b.get("repro") or []
    if isinstance(blocks, str): blocks = [{"title": "Reproduce", "code": blocks}]
    return "".join(f'<div class="repro"><div class="repro-h">{esc(x.get("title","Reproduce"))} '
                   f'<span class="mut">— run via <code>panda execute</code></span></div>'
                   f'<pre>{esc(x["code"])}</pre></div>' for x in blocks)

def part(k, frag): return f'<h3>{esc(k)}</h3>{frag}' if frag else ""

def bug_section(b):
    sev = b.get("severity", "minor")
    badges = badge(sev, sev) + "".join(badge(x["text"], x["kind"]) for x in b.get("status_badges", []))
    charts = "".join(sparkline(s) for s in b.get("series", []))
    cites = "".join(f"<li><code>{esc(c)}</code></li>" for c in b.get("citations", []))
    cites = f'<h3>Citations</h3><ul class="cites">{cites}</ul>' if cites else ""
    c = b.get("connections", {})
    up = int(b.get('upvotes', 0))
    return f"""<section class="bug" id="{esc(b['id'])}" data-id="{esc(b['id'])}" data-base="{up}"
      data-sev="{esc(sev)}" data-clients="{esc(' '.join(c.get('clients',[])).lower())}"
      data-haspr="{'1' if c.get('prs') else '0'}" data-up="{up}"
      data-text="{esc((b['title']+' '+b.get('subtitle','')).lower())}">
      <div class="bug-h"><h2>{esc(b['id'])} — {esc(b['title'])}</h2>
        <button class="upbtn" data-vote="{esc(b['id'])}" title="upvote — saved in your browser">▲ <span class="up-n">{up}</span></button></div>
      <p class="sub">{esc(b.get('subtitle',''))}</p>
      <div class="meta">{badges}</div>
      {connections(b)}
      {part('Overview', b.get('overview_html',''))}
      {part('Root cause', b.get('root_cause_html',''))}
      {part('Discovery timeline', timeline(b.get('timeline', [])))}
      {charts}
      {repro(b)}
      {part('Fix', b.get('fix_html',''))}
      {cites}
    </section>"""

# ---- leaderboard, ranked by upvotes then severity ----
SEV_RANK = {"critical":0,"major":1,"minor":2}
bugs.sort(key=lambda b: (-int(b.get("upvotes",0)), SEV_RANK.get(b.get("severity"),9)))
def row(b):
    c = b.get("connections", {})
    prs = " ".join(badge(f"#{p['number']} {p['state']}", PR_KIND.get(p['state'],"minor")) for p in c.get("prs",[]))
    up = int(b.get("upvotes", 0))
    return (f'<tr data-id="{esc(b["id"])}" data-base="{up}" data-sev="{esc(b.get("severity"))}" '
            f'data-haspr="{"1" if c.get("prs") else "0"}" '
            f'data-clients="{esc(" ".join(c.get("clients",[])).lower())}" data-up="{up}" '
            f'data-text="{esc(b["title"].lower())}">'
            f'<td><button class="upbtn" data-vote="{esc(b["id"])}" title="upvote — saved in your browser">▲ <span class="up-n">{up}</span></button></td>'
            f'<td>{badge(b.get("severity"), b.get("severity"))}</td>'
            f'<td>{a("#"+b["id"], b["id"]+" — "+b["title"], ext=False)}</td>'
            f'<td>{esc(", ".join(c.get("clients",[])))}</td><td>{prs}</td></tr>')
clients = sorted({cl for b in bugs for cl in b.get("connections",{}).get("clients",[])})

CSS = r"""
:root{--bg:#f7f8fa;--panel:#ffffff;--panel2:#f1f2f4;--line:#e3e6ea;--line2:#eef0f2;--ink:#15181d;
--mut:#5b6470;--faint:#8a929e;--accent:#4f5bd5;--ok:#16a34a;--ok-bg:#eafbf0;--bad:#dc2626;--bad-bg:#fdeeee;
--amber:#ca8a04;--amber-bg:#fef6e0;--purple:#8250df;--purple-bg:#f6f0fe;--blue:#2f6feb;--blue-bg:#eaf1fe;
--mono:"JetBrains Mono",ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;
--sans:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Inter,sans-serif;color-scheme:light;}
:root[data-theme=dark]{--bg:#0d1117;--panel:#161b22;--panel2:#21262d;--line:#30363d;--line2:#21262d;--ink:#e6edf3;
--mut:#9aa3af;--faint:#6b7280;--accent:#7c86f0;--ok:#3fb950;--ok-bg:#0f2417;--bad:#f85149;--bad-bg:#2a1213;
--amber:#e3b341;--amber-bg:#2a2210;--purple:#b083f0;--purple-bg:#211a33;--blue:#58a6ff;--blue-bg:#12233d;color-scheme:dark;}
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:var(--sans);background:var(--bg);color:var(--ink);line-height:1.6;padding:2rem;-webkit-font-smoothing:antialiased}
.container{max-width:980px;margin:0 auto}
h1{font-size:1.4rem;letter-spacing:-.01em;margin-bottom:.2rem}
h2{font-size:1.05rem;letter-spacing:-.01em}h3{font-size:.78rem;text-transform:uppercase;letter-spacing:.08em;color:var(--faint);margin:1rem 0 .5rem}
.sub{color:var(--mut);font-size:.9rem;margin-bottom:.8rem}.mut{color:var(--mut)}
a{color:var(--accent);text-decoration:none}a:hover{text-decoration:underline}
.badge{display:inline-flex;align-items:center;padding:.12rem .55rem;border-radius:999px;font-size:.72rem;font-weight:600;margin:0 .25rem .25rem 0;border:1px solid transparent}
.b-critical,.b-open{background:var(--bad-bg);color:var(--bad);border-color:var(--bad)}
.b-major{background:var(--amber-bg);color:var(--amber);border-color:var(--amber)}
.b-minor,.b-label{background:var(--panel2);color:var(--mut);border-color:var(--line)}
.b-confirmed,.b-investigating{background:var(--blue-bg);color:var(--blue);border-color:var(--blue)}
.b-draft{background:var(--purple-bg);color:var(--purple);border-color:var(--purple)}
.b-fixed{background:var(--ok-bg);color:var(--ok);border-color:var(--ok)}
.panel,.bug{background:var(--panel);border:1px solid var(--line);border-radius:10px;padding:1.1rem 1.3rem;margin-bottom:1.1rem}
.bug{scroll-margin-top:1rem}.meta{margin:.4rem 0 .6rem}
.kv{display:grid;grid-template-columns:150px 1fr;gap:.35rem 1rem;font-size:.88rem;margin:.4rem 0 .2rem}
.kv .k{color:var(--faint)}code{background:var(--panel2);border:1px solid var(--line);border-radius:5px;padding:.05em .35em;font:.85em var(--mono)}
p{font-size:.9rem;margin-bottom:.6rem}ul,ol{padding-left:1.3rem;font-size:.9rem;margin-bottom:.6rem}
pre{background:var(--panel2);border:1px solid var(--line);border-radius:8px;padding:.8rem 1rem;overflow-x:auto;font:.8rem var(--mono);color:var(--ink);white-space:pre;margin:.5rem 0}
table{width:100%;border-collapse:collapse;font-size:.86rem}
th{text-align:left;color:var(--faint);font-size:.72rem;text-transform:uppercase;letter-spacing:.05em;padding:.5rem .6rem;border-bottom:1px solid var(--line)}
th[data-sort]{cursor:pointer;user-select:none}th[data-sort]:hover{color:var(--ink)}
td{padding:.55rem .6rem;border-bottom:1px solid var(--line2);vertical-align:top}
tbody tr:hover{background:var(--panel2)}tr[hidden]{display:none}
.upbtn{display:inline-flex;align-items:center;gap:.25rem;background:var(--panel2);color:var(--mut);border:1px solid var(--line);border-radius:999px;padding:.15rem .55rem;font:600 .78rem var(--mono);cursor:pointer;font-variant-numeric:tabular-nums;white-space:nowrap}
.upbtn:hover{border-color:var(--accent);color:var(--accent)}
.upbtn.voted{background:var(--purple-bg);color:var(--accent);border-color:var(--accent)}
.bug-h{display:flex;justify-content:space-between;align-items:flex-start;gap:1rem}
.fork-table td.canonical,.canonical{color:var(--ok);font-weight:600}.divergent,.warn{color:var(--bad);font-weight:600}
.delta{font:600 .9em var(--mono);color:var(--bad)}
.timeline{border-left:2px solid var(--line);padding-left:1.1rem;margin:.4rem 0}
.tl-item{position:relative;margin-bottom:.9rem;font-size:.88rem}
.tl-item::before{content:"";position:absolute;left:-1.35rem;top:.45rem;width:8px;height:8px;border-radius:50%;background:var(--accent);border:2px solid var(--panel)}
.tl-restart::before{background:var(--bad)}.tl-timing::before{background:var(--amber)}.tl-block::before{background:var(--ok)}
.tl-date{font-size:.76rem;color:var(--faint);margin-bottom:.15rem}
.chart{margin:.6rem 0}.chart figcaption{font-size:.78rem;color:var(--mut);margin-bottom:.2rem}
.chart svg{width:100%;height:90px;background:var(--panel2);border:1px solid var(--line);border-radius:8px}
.spark{fill:none;stroke:var(--accent);stroke-width:1.5;vector-effect:non-scaling-stroke}
.ev{stroke:var(--amber);stroke-width:1;stroke-dasharray:3 2;vector-effect:non-scaling-stroke}
.repro{margin:.5rem 0}.repro-h{font-size:.82rem;margin-bottom:.2rem}.cites{color:var(--mut);font-size:.82rem}
#bar{display:flex;flex-wrap:wrap;gap:.5rem;align-items:center;margin-bottom:1rem}
#bar input,#bar select{background:var(--panel);color:var(--ink);border:1px solid var(--line);border-radius:8px;padding:.4rem .55rem;font-size:.85rem}
#bar input[type=search]{flex:1 1 200px}#bar input:focus,#bar select:focus{outline:1px solid var(--accent);outline-offset:0}
.chips{display:flex;gap:.35rem;flex-wrap:wrap}
.chip{cursor:pointer;user-select:none;padding:.18rem .6rem;border-radius:999px;font-size:.74rem;font-weight:600;border:1px solid var(--line);background:var(--panel);color:var(--mut)}
.chip[aria-pressed=true].c-critical{background:var(--bad-bg);color:var(--bad);border-color:var(--bad)}
.chip[aria-pressed=true].c-major{background:var(--amber-bg);color:var(--amber);border-color:var(--amber)}
.chip[aria-pressed=true].c-minor{background:var(--panel2);color:var(--ink);border-color:var(--faint)}
#theme{cursor:pointer;margin-left:auto}
.note{font-size:.8rem;color:var(--mut);border:1px dashed var(--line);border-radius:8px;padding:.6rem .8rem;margin-bottom:1rem}
"""

JS = r"""
const $=s=>document.querySelector(s),$$=s=>[...document.querySelectorAll(s)];
const ls=(k,d)=>{try{return localStorage.getItem(k)??d}catch(e){return d}};
const lset=(k,v)=>{try{localStorage.setItem(k,v)}catch(e){}};
const t0=ls('panda-bug-theme');if(t0)document.documentElement.dataset.theme=t0;
$('#theme').onclick=()=>{const d=document.documentElement.dataset;
  d.theme=d.theme==='dark'?'light':'dark';lset('panda-bug-theme',d.theme);};

// upvotes: bake base counts, persist the viewer's own vote locally; count = base + own.
let votes=new Set(JSON.parse(ls('panda-bug-votes','[]')));
function eff(el){return +el.dataset.base+(votes.has(el.dataset.id)?1:0);}
function paint(){$$('[data-id]').forEach(el=>{const n=eff(el);el.dataset.up=n;
  el.querySelectorAll('.up-n').forEach(x=>x.textContent=n);
  el.querySelectorAll('.upbtn').forEach(b=>b.classList.toggle('voted',votes.has(el.dataset.id)));});sortIf();}
document.addEventListener('click',e=>{const b=e.target.closest('.upbtn');if(!b)return;
  const id=b.dataset.vote;votes.has(id)?votes.delete(id):votes.add(id);
  lset('panda-bug-votes',JSON.stringify([...votes]));paint();});

// filters: search + severity chips + client + PR state
let sevOn=new Set();
$$('#chips .chip').forEach(c=>c.onclick=()=>{const s=c.dataset.sev;
  sevOn.has(s)?sevOn.delete(s):sevOn.add(s);c.setAttribute('aria-pressed',sevOn.has(s));apply();});
function apply(){const q=($('#q').value||'').toLowerCase(),cl=$('#f-cli').value,pr=$('#f-pr').value;
  $$('#board tbody tr').forEach(r=>{const d=r.dataset;
    r.hidden=(q&&!d.text.includes(q)&&!d.clients.includes(q))||(sevOn.size&&!sevOn.has(d.sev))
      ||(cl&&!d.clients.includes(cl))||(pr==='has'&&d.haspr!=='1')||(pr==='none'&&d.haspr!=='0');});}
['#q','#f-cli','#f-pr'].forEach(s=>$(s).addEventListener('input',apply));

// sort: click a header; default is upvotes desc
let sortKey='up',sortDesc=true;
function sortIf(){const tb=$('#board tbody');if(!tb)return;
  [...tb.rows].sort((a,b)=>{const x=a.dataset[sortKey],y=b.dataset[sortKey],n=+x-+y,
    c=isNaN(n)?(x<y?-1:1):n;return sortDesc?-c:c;}).forEach(r=>tb.appendChild(r));}
$$('#board th[data-sort]').forEach(th=>th.onclick=()=>{const k=th.dataset.sort;
  sortDesc=k===sortKey?!sortDesc:true;sortKey=k;sortIf();});
paint();
"""

generated = datetime.utcnow().isoformat() + "Z"
head = ('<!DOCTYPE html><html lang="en"><head><meta charset="UTF-8">'
        '<meta name="viewport" content="width=device-width,initial-scale=1">'
        '<meta name="color-scheme" content="light dark">'
        f'<title>{esc(NETWORK)} · bug board</title><style>' + CSS + '</style></head><body><div class="container">')
header = (f'<h1>{esc(NETWORK)} — consensus bug board</h1>'
          f'<p class="sub">Window {esc(WINDOW)} · generated {esc(generated)} · {len(bugs)} bug(s) · <a id="theme">◐ theme</a></p>'
          '<div class="note">Static snapshot. Your upvote is saved in this browser; shared vote totals, comments '
          'and live ClickHouse log/chart streaming need the bug-board backend. This page bakes the snapshot and links out to live tooling.</div>')
bar = ('<div id="bar"><input id="q" type="search" placeholder="search bugs / clients…">'
       '<div id="chips" class="chips">'
       '<span class="chip c-critical" data-sev="critical" aria-pressed="false">critical</span>'
       '<span class="chip c-major" data-sev="major" aria-pressed="false">major</span>'
       '<span class="chip c-minor" data-sev="minor" aria-pressed="false">minor</span></div>'
       '<select id="f-cli"><option value="">all clients</option>' +
       "".join(f'<option>{esc(c)}</option>' for c in clients) +
       '</select><select id="f-pr"><option value="">any PR state</option>'
       '<option value="has">has PR</option><option value="none">no PR</option></select></div>')
board = ('<table id="board" class="panel"><thead><tr>'
         '<th data-sort="up">▲ Upvotes</th><th data-sort="sev">Severity</th><th>Bug</th>'
         '<th data-sort="clients">Clients</th><th>PRs</th></tr></thead><tbody>'
         + "".join(row(b) for b in bugs) + '</tbody></table>')
doc = head + header + bar + board + "\n".join(bug_section(b) for b in bugs) + "<script>" + JS + "</script></div></body></html>"

path = f"/workspace/{NETWORK}-bugboard-{datetime.utcnow().strftime('%Y%m%d-%H%M%S')}.html"
open(path, "w").write(doc)
print(storage.upload(path, remote_name=path.split("/")[-1]).url)
print(path)
```

Every field is `html.escape`'d, so log lines and titles cannot break out of the page. (If you later switch to an injected-JSON viewer for live mode, escape the blob with `json.dumps(...).replace("</", "<\\/")` as `report_template.html` does.)

Deliver the published URL and the local `/workspace/...` path. State plainly that the page is a snapshot and which features (upvotes, comments, live streaming) need the backend. If the network is healthy, still publish a board whose summary states zero bugs and shows the baseline. Only push to the external starflinger asset store on explicit request.
