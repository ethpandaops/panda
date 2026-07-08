---
name: Render the Devnet Bug Board HTML
description: Render bug objects (/workspace/bugs.json — issue records plus board fields from the devnet bug scan) into one self-contained interactive HTML bug board — a leaderboard with upvote buttons (persisted in localStorage), severity/client/PR filter chips, click-to-sort columns, and per-bug detail with connections, discovery timeline, inline SVG charts, and embedded reproduction scripts — then publish it with the storage library. Every field is HTML-escaped; cross-references link to Dora slots, ethpandaops instance pages, GitHub PRs and source lines, and EIPs at commit.
tags: [devnet, bug-report, html, leaderboard, render, publish]
triggers:
  - render bug objects into an html bug board
  - html bug leaderboard with upvotes severity client filters
  - publish a static devnet bug board from bugs json
  - convert issue records into an interactive html report
---

Owns the bug board's **presentation layer**: consumes `/workspace/bugs.json` produced
by `runbooks://devnet_bug_report` (which owns the scan, severity rubric, and bug-object
schema) and emits one published, self-contained HTML file. The generator below owns
page chrome, CSS tokens, the leaderboard, the cross-reference link vocabulary, and all
rendering — bug narrative arrives as plain text and is HTML-escaped without exception.

## Inputs

Required: `/workspace/bugs.json`, plus `NETWORK`, `WINDOW` (the pinned slot/epoch
range), and `BASELINE` (the one-line health baseline from the scan).
Preferred: the network's service URLs from the `networks://<network>` resource
(authority: `runbooks://public_devnet_context`) — set `DORA_BASE` from the published
Dora URL; the `dora.<network>.ethpandaops.io` convention is only a fallback.

## Output

One published HTML file: the storage URL and the host path, delivered together
(upload conventions: `runbooks://sandbox_output_conventions`).

It is a **static snapshot**. The viewer's own upvote is saved in their browser;
shared vote totals, comments, and live log/chart streaming need the bug-board
backend — the page bakes a snapshot and links out to live tooling. Say so plainly in
the delivered report; do not imply the file is live or that upvotes are shared.

The visual language follows the EthPandaOps bug-leaderboard product: light theme by
default (indigo accent, JetBrains-Mono-first code, soft tinted status/severity chips),
dark variant via the "◐ theme" toggle (persisted in `localStorage`; deliberately no
OS-preference auto-switch). Architecturally: CSS design tokens, zero external fetches,
safe as an uploaded asset.

## Cross-Reference Vocabulary

Every concrete artifact links to live ethpandaops tooling. **Prefer service URLs from
`networks://<network>`**; fall back to the conventional patterns below only when the
resource omits them.

| Target | URL pattern | Display |
| --- | --- | --- |
| Dora slot | `https://dora.<net>.ethpandaops.io/slot/<n>` | `Slot <n>` |
| Dora epoch / validator / block | `…/epoch|validator|block/<id>` | `Epoch <n>` etc. |
| Instance | `https://ethpandaops.io/networks/<net>/?tab=instances&instance=<name>` | `ssh devops@<name>.srv.<net>.ethpandaops.io` |
| GitHub PR | `https://github.com/<repo>/pull/<n>` | `<repo>#<n>` + state badge |
| GitHub commit | `https://github.com/<repo>/commit/<sha>` | `<sha[:9]>` |
| Source line(s) | `https://github.com/<repo>/blob/<ref>/<path>#L<a>-L<b>` | `<file>:<a>` |
| EIP at commit | `https://github.com/ethereum/EIPs/blob/<commit>/EIPS/eip-<n>.md` | `EIP-<n> @ <commit[:7]>` |
| Kurtosis config | `https://github.com/ethpandaops/<network>/blob/<ref>/network-params.yaml` | `ethereum-package config` |

Embed a reproducing Kurtosis config link only when the reproduction was actually
verified.

## Generator

Run verbatim in the sandbox after filling `NETWORK`, `WINDOW`, `BASELINE`, and
`DORA_BASE`. Every rendered field — including the `*_text` narrative fields — goes
through `html.escape`, so no bug content can break out of the page.

```python
import html, json
from datetime import datetime, timezone
from ethpandaops import storage

NETWORK = "<network>"
WINDOW  = "<slot/epoch range>"
BASELINE = "<one-line health baseline: split? finalizing? participating?>"
# Set from the Dora service URL in networks://<network>; the convention is a fallback.
DORA_BASE = f"https://dora.{NETWORK}.ethpandaops.io"
bugs = json.load(open("/workspace/bugs.json"))

def esc(s): return html.escape("" if s is None else str(s))
def para(t):
    if not t: return ""
    return "".join(f'<p>{esc(p).replace(chr(10), "<br>")}</p>' for p in str(t).split("\n\n"))

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
    return a(f"https://github.com/ethereum/EIPs/blob/{commit}/EIPS/eip-{n}.md", f"EIP-{n} @ {esc(commit[:7])}")

# ---- render helpers ----
def badge(text, kind): return f'<span class="badge b-{esc(kind)}">{esc(text)}</span>'
PR_KIND = {"merged":"fixed","open":"open","draft":"draft","closed":"minor"}

def connections(b):
    board = b.get("board", {})
    c, rows = board.get("connections", {}), []
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
    if board.get("labels"): r("Labels", " ".join(badge(l, "label") for l in board["labels"]))
    return f'<div class="kv">{"".join(rows)}</div>' if rows else ""

def timeline(events):
    if not events: return ""
    items = []
    for e in events:
        ref = " " + dora_slot(e["slot"]) if e.get("slot") is not None else ""
        pre = f'<pre>{esc(e["log"])}</pre>' if e.get("log") else ""
        items.append(f'<div class="tl-item tl-{esc(e.get("kind","note"))}">'
                     f'<div class="tl-date">{esc(e.get("ts",""))} · {esc(e.get("kind","note"))}{ref}</div>'
                     f'<div>{esc(e.get("text",""))}</div>{pre}</div>')
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
                   f'<span class="mut">— run in the panda sandbox</span></div>'
                   f'<pre>{esc(x["code"])}</pre></div>' for x in blocks)

def part(k, frag): return f'<h3>{esc(k)}</h3>{frag}' if frag else ""

def evidence_list(issue):
    items = []
    for e in issue.get("evidence", []):
        anchor = " — ".join(x for x in [e.get("at",""), e.get("detail","")] if x)
        items.append(f'<li><code>{esc(e.get("source",""))}: {esc(e.get("ref",""))}</code> — {esc(anchor)}</li>')
    return f'<h3>Evidence</h3><ul class="cites">{"".join(items)}</ul>' if items else ""

def sev_of(b): return b.get("severity") or "minor"

def bug_section(b):
    issue, board = b["issue"], b.get("board", {})
    sev = sev_of(b)
    badges = (badge(sev, sev) + badge(f"confidence: {issue.get('confidence','low')}", "label")
              + "".join(badge(x["text"], x["kind"]) for x in board.get("status_badges", [])))
    charts = "".join(sparkline(s) for s in b.get("series", []))
    overview = para(board.get("overview_text") or issue.get("summary", ""))
    c = board.get("connections", {})
    up = int(board.get('upvotes', 0))
    return f"""<section class="bug" id="{esc(b['id'])}" data-id="{esc(b['id'])}" data-base="{up}"
      data-sev="{esc(sev)}" data-clients="{esc(' '.join(c.get('clients',[])).lower())}"
      data-haspr="{'1' if c.get('prs') else '0'}" data-up="{up}"
      data-text="{esc(' '.join([issue['title'], board.get('subtitle','')] + board.get('labels', [])).lower())}">
      <div class="bug-h"><h2>{esc(b['id'])} — {esc(issue['title'])}</h2>
        <button class="upbtn" data-vote="{esc(b['id'])}" title="upvote — saved in your browser">▲ <span class="up-n">{up}</span></button></div>
      <p class="sub">{esc(board.get('subtitle',''))}</p>
      <div class="meta">{badges}</div>
      {connections(b)}
      {part('Overview', overview)}
      {part('Root cause', para(board.get('root_cause_text','')))}
      {part('Discovery timeline', timeline(b.get('timeline', [])))}
      {charts}
      {repro(b)}
      {part('Fix', para(board.get('fix_text','')))}
      {evidence_list(issue)}
    </section>"""

# ---- leaderboard, ranked by upvotes then severity ----
SEV_RANK = {"critical":0,"major":1,"minor":2}
bugs.sort(key=lambda b: (-int(b.get("board", {}).get("upvotes", 0)), SEV_RANK.get(sev_of(b), 9)))
def row(b):
    issue, board = b["issue"], b.get("board", {})
    c = board.get("connections", {})
    prs = " ".join(badge(f"#{p['number']} {p['state']}", PR_KIND.get(p['state'],"minor")) for p in c.get("prs",[]))
    up = int(board.get("upvotes", 0))
    return (f'<tr data-id="{esc(b["id"])}" data-base="{up}" data-sev="{esc(sev_of(b))}" '
            f'data-sevrank="{2 - SEV_RANK.get(sev_of(b), 9)}" '
            f'data-haspr="{"1" if c.get("prs") else "0"}" '
            f'data-clients="{esc(" ".join(c.get("clients",[])).lower())}" data-up="{up}" '
            f'data-text="{esc(" ".join([issue["title"]] + board.get("labels", [])).lower())}">'
            f'<td><button class="upbtn" data-vote="{esc(b["id"])}" title="upvote — saved in your browser">▲ <span class="up-n">{up}</span></button></td>'
            f'<td>{badge(sev_of(b), sev_of(b))}</td>'
            f'<td>{a("#"+b["id"], b["id"]+" — "+issue["title"], ext=False)}</td>'
            f'<td>{esc(", ".join(c.get("clients",[])))}</td><td>{prs}</td></tr>')
clients = sorted({cl for b in bugs for cl in b.get("board", {}).get("connections", {}).get("clients", [])})

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
.container{max-width:980px;margin:0 auto}h1{font-size:1.4rem;letter-spacing:-.01em;margin-bottom:.2rem}
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
tbody tr:hover{background:var(--panel2)}tr[hidden]{display:none}.bug[hidden]{display:none}
.upbtn{display:inline-flex;align-items:center;gap:.25rem;background:var(--panel2);color:var(--mut);border:1px solid var(--line);border-radius:999px;padding:.15rem .55rem;font:600 .78rem var(--mono);cursor:pointer;font-variant-numeric:tabular-nums;white-space:nowrap}
.upbtn:hover{border-color:var(--accent);color:var(--accent)}
.upbtn.voted{background:var(--purple-bg);color:var(--accent);border-color:var(--accent)}
.bug-h{display:flex;justify-content:space-between;align-items:flex-start;gap:1rem}
.timeline{border-left:2px solid var(--line);padding-left:1.1rem;margin:.4rem 0}.tl-item{position:relative;margin-bottom:.9rem;font-size:.88rem}
.tl-item::before{content:"";position:absolute;left:-1.35rem;top:.45rem;width:8px;height:8px;border-radius:50%;background:var(--accent);border:2px solid var(--panel)}
.tl-restart::before{background:var(--bad)}.tl-timing::before{background:var(--amber)}.tl-block::before{background:var(--ok)}
.tl-date{font-size:.76rem;color:var(--faint);margin-bottom:.15rem}
.chart{margin:.6rem 0}.chart figcaption{font-size:.78rem;color:var(--mut);margin-bottom:.2rem}.chart svg{width:100%;height:90px;background:var(--panel2);border:1px solid var(--line);border-radius:8px}
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
#theme{cursor:pointer;margin-left:auto}.note{font-size:.8rem;color:var(--mut);border:1px dashed var(--line);border-radius:8px;padding:.6rem .8rem;margin-bottom:1rem}
"""

JS = r"""
const $=s=>document.querySelector(s),$$=s=>[...document.querySelectorAll(s)];
const ls=(k,d)=>{try{return localStorage.getItem(k)??d}catch(e){return d}};
const lset=(k,v)=>{try{localStorage.setItem(k,v)}catch(e){}};
const t0=ls('panda-bug-theme');if(t0)document.documentElement.dataset.theme=t0;
$('#theme').onclick=()=>{const d=document.documentElement.dataset;
  d.theme=d.theme==='dark'?'light':'dark';lset('panda-bug-theme',d.theme);};

// upvotes: bake base counts, persist the viewer's own vote locally; count = base + own.
let votes=new Set();try{votes=new Set(JSON.parse(ls('panda-bug-votes','[]')))}catch(e){}
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
  const hide=d=>(q&&!d.text.includes(q)&&!d.clients.includes(q))||(sevOn.size&&!sevOn.has(d.sev))
      ||(cl&&!d.clients.includes(cl))||(pr==='has'&&d.haspr!=='1')||(pr==='none'&&d.haspr!=='0');
  $$('#board tbody tr').forEach(r=>{if(r.dataset.id)r.hidden=hide(r.dataset);});
  $$('section.bug').forEach(s=>{s.hidden=hide(s.dataset);});}
['#q','#f-cli','#f-pr'].forEach(s=>$(s).addEventListener('input',apply));

// sort: click a header; default is upvotes desc
let sortKey='up',sortDesc=true;
function sortIf(){const tb=$('#board tbody');if(!tb)return;
  [...tb.rows].sort((a,b)=>{const x=a.dataset[sortKey],y=b.dataset[sortKey],n=+x-+y,
    c=isNaN(n)?(x<y?-1:1):n;
    if(c===0&&sortKey!=='sevrank')return (+b.dataset.sevrank)-(+a.dataset.sevrank);
    return sortDesc?-c:c;}).forEach(r=>tb.appendChild(r));}
$$('#board th[data-sort]').forEach(th=>th.onclick=()=>{const k=th.dataset.sort;
  sortDesc=k===sortKey?!sortDesc:true;sortKey=k;sortIf();});
paint();
"""

generated = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
head = ('<!DOCTYPE html><html lang="en"><head><meta charset="UTF-8">'
        '<meta name="viewport" content="width=device-width,initial-scale=1">'
        '<meta name="color-scheme" content="light dark">'
        f'<title>{esc(NETWORK)} · bug board</title><style>' + CSS + '</style></head><body><div class="container">')
header = (f'<h1>{esc(NETWORK)} — consensus bug board</h1>'
          f'<p class="sub">Window {esc(WINDOW)} · generated {esc(generated)} · {len(bugs)} bug(s) · <a id="theme">◐ theme</a></p>'
          f'<p class="sub">Baseline: {esc(BASELINE)}</p>'
          '<div class="note">Static snapshot. Your upvote is saved in this browser; shared vote totals, comments '
          'and live log/chart streaming need the bug-board backend. This page bakes the snapshot and links out to live tooling.</div>')
bar = ('<div id="bar"><input id="q" type="search" placeholder="search bugs / clients…">'
       '<div id="chips" class="chips">'
       '<span class="chip c-critical" data-sev="critical" aria-pressed="false">critical</span>'
       '<span class="chip c-major" data-sev="major" aria-pressed="false">major</span>'
       '<span class="chip c-minor" data-sev="minor" aria-pressed="false">minor</span></div>'
       '<select id="f-cli"><option value="">all clients</option>' +
       "".join(f'<option value="{esc(c.lower())}">{esc(c)}</option>' for c in clients) +
       '</select><select id="f-pr"><option value="">any PR state</option>'
       '<option value="has">has PR</option><option value="none">no PR</option></select></div>')
rows = "".join(row(b) for b in bugs) or \
    '<tr><td colspan="5" class="mut">no bugs in this window — baseline only</td></tr>'
board = ('<table id="board" class="panel"><thead><tr>'
         '<th data-sort="up">▲ Upvotes</th><th data-sort="sevrank">Severity</th><th>Bug</th>'
         '<th data-sort="clients">Clients</th><th>PRs</th></tr></thead><tbody>'
         + rows + '</tbody></table>')
doc = head + header + bar + board + "\n".join(bug_section(b) for b in bugs) + "<script>" + JS + "</script></div></body></html>"

path = f"/workspace/{NETWORK}-bugboard-{datetime.now(timezone.utc).strftime('%Y%m%d-%H%M%S')}.html"
open(path, "w").write(doc)
up = storage.upload(path, remote_name=path.split("/")[-1])
print(up.url)
print(up.host_path)
```

There are no raw-HTML inputs: `overview_text`, `root_cause_text`, and `fix_text` are
plain text rendered as escaped paragraphs, and every structured field is
`html.escape`'d, so log lines, titles, and timeline text cannot break out of the page.
(If you later switch to an injected-JSON viewer for live mode, escape the blob with
`json.dumps(...).replace("</", "<\\/")` so a `</script>` inside the data cannot
terminate the script tag.)

## Self-Check

Before delivering:

- `DORA_BASE` came from the `networks://<network>` service URL when available; the
  convention was used only as a stated fallback.
- Both the published URL and the host path were delivered.
- The published URL was fetched back and the page contains every bug id, the
  baseline line, and the static-snapshot note.
- The delivered message states the page is a static snapshot: upvotes are
  viewer-local, and nothing on it streams live data.
- No unescaped bug content reached the page — narrative arrived as `*_text` and went
  through the paragraph renderer.
