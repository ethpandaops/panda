"""Self-contained HTML report for a release-qualification run.

``build_html`` renders everything the markdown scorecard summarizes — plus the detail
it can't fit: per-run results with full grader reasons, token/score percentile
distributions, per-category (tag) breakdowns, and the cross-release trend — into one
dependency-free HTML file. Charts are embedded as base64 PNGs, so the file works as a
release asset, a CI artifact, or a GitHub Pages page with no external fetches.
"""

from __future__ import annotations

import base64
import html
import statistics
from pathlib import Path


def _esc(s: object) -> str:
    return html.escape(str(s))


def _pct(values: list[float], q: float) -> float:
    """Percentile with linear interpolation; safe on small samples."""
    if not values:
        return 0.0
    ordered = sorted(values)
    if len(ordered) == 1:
        return float(ordered[0])
    pos = (len(ordered) - 1) * q
    lo = int(pos)
    hi = min(lo + 1, len(ordered) - 1)
    return float(ordered[lo] + (ordered[hi] - ordered[lo]) * (pos - lo))


def token_percentiles(runs: list[dict]) -> dict[str, float]:
    """Token distribution over correct runs (the cost of a right answer)."""
    tokens = [float(r["tokens"]) for r in runs if r["correct"] and r.get("tokens", 0) > 0]
    return {
        "p10": round(_pct(tokens, 0.10), 1),
        "p50": round(_pct(tokens, 0.50), 1),
        "p90": round(_pct(tokens, 0.90), 1),
        "p99": round(_pct(tokens, 0.99), 1),
        "max": round(max(tokens), 1) if tokens else 0.0,
    }


def category_breakdown(runs: list[dict], tags_by_question: dict[str, list[str]]) -> list[dict]:
    """Per-tag aggregates. A run counts toward every tag its question carries."""
    cats: dict[str, dict] = {}
    for run in runs:
        for tag in tags_by_question.get(run["id"], ["untagged"]):
            cat = cats.setdefault(
                tag, {"tag": tag, "runs": 0, "correct": 0, "tokens": [], "questions": set()}
            )
            cat["runs"] += 1
            cat["questions"].add(run["id"])
            if run["correct"]:
                cat["correct"] += 1
                if run.get("tokens", 0) > 0:
                    cat["tokens"].append(run["tokens"])
    out = []
    for cat in sorted(cats.values(), key=lambda c: (c["correct"] / c["runs"], c["tag"])):
        out.append(
            {
                "tag": cat["tag"],
                "questions": len(cat["questions"]),
                "runs": cat["runs"],
                "correct": cat["correct"],
                "pass_rate": round(cat["correct"] / cat["runs"], 4),
                "median_tokens_correct": round(statistics.median(cat["tokens"]), 1)
                if cat["tokens"]
                else 0.0,
            }
        )
    return out


def _img(path: Path) -> str:
    if not path.exists():
        return ""
    b64 = base64.b64encode(path.read_bytes()).decode()
    return f'<img alt="{_esc(path.stem)}" src="data:image/png;base64,{b64}">'


def _headline(record: dict, questions: dict[str, dict]) -> str:
    n_correct = round(record["pass_rate"] * record["runs"])
    cards = [
        ("pass rate", f"{record['pass_rate']:.0%}", f"{n_correct}/{record['runs']} runs"),
        ("mean score", f"{record['mean_score']:.3f}", "token-efficiency, correctness-gated"),
        ("questions", str(len(questions)), record.get("cases", "")),
        (
            "tokens p50 / p90",
            f"{record['token_percentiles']['p50']:,.0f} / {record['token_percentiles']['p90']:,.0f}",
            "correct runs",
        ),
    ]
    cells = "".join(
        f'<div class="card"><div class="k">{_esc(k)}</div>'
        f'<div class="v">{_esc(v)}</div><div class="s">{_esc(s)}</div></div>'
        for k, v, s in cards
    )
    return f'<div class="cards">{cells}</div>'


def _history_table(record: dict, history: list[dict]) -> str:
    rows = []
    for entry, current in [(record, True)] + [(e, False) for e in reversed(history)]:
        n_ok = round(entry["pass_rate"] * entry["runs"])
        pcts = entry.get("token_percentiles") or {}
        p50 = f"{pcts['p50']:,.0f}" if pcts.get("p50") else f"{entry['mean_tokens_correct']:,.0f}*"
        tag = f"<strong>{_esc(entry['tag'])} (this release)</strong>" if current else _esc(entry["tag"])
        rows.append(
            f"<tr{' class=current' if current else ''}><td>{tag}</td>"
            f"<td>{entry['pass_rate']:.0%} ({n_ok}/{entry['runs']})</td>"
            f"<td>{entry['mean_score']:.3f}</td><td>{p50}</td></tr>"
        )
    return (
        "<table><thead><tr><th>release</th><th>pass rate</th><th>mean score</th>"
        "<th>tokens p50 (correct)</th></tr></thead><tbody>" + "".join(rows) + "</tbody></table>"
        "<p class=note>* older records predate percentiles; mean shown.</p>"
    )


def _category_table(categories: list[dict]) -> str:
    rows = "".join(
        f"<tr><td>{_esc(c['tag'])}</td><td>{c['questions']}</td>"
        f"<td>{c['correct']}/{c['runs']} ({c['pass_rate']:.0%})</td>"
        f"<td>{c['median_tokens_correct']:,.0f}</td></tr>"
        for c in categories
    )
    return (
        "<table><thead><tr><th>category</th><th>questions</th><th>pass rate</th>"
        "<th>median tokens (correct)</th></tr></thead><tbody>" + rows + "</tbody></table>"
    )


def _question_matrix(runs: list[dict], prev: dict | None) -> str:
    """One row per question: a cell per run (✓/✗ with grader reason on hover), plus the
    flip verdict against the previous qualified release."""
    by_q: dict[str, list[dict]] = {}
    for r in runs:
        by_q.setdefault(r["id"], []).append(r)
    prev_q = (prev or {}).get("questions", {})

    rows = []
    for qid in sorted(by_q, key=lambda q: sum(r["correct"] for r in by_q[q]) / len(by_q[q])):
        cells = []
        for r in by_q[qid]:
            mark = "✓" if r["correct"] else "✗"
            cls = "ok" if r["correct"] else "bad"
            reason = _esc((r.get("grader_reason") or "")[:600])
            tokens = f"{r.get('tokens', 0):,}tok"
            link = f' <a href="{_esc(r["trace_url"])}">↗</a>' if r.get("trace_url") else ""
            cells.append(
                f'<span class="run {cls}" title="{reason}">{mark}'
                f'<span class="tok">{tokens}</span>{link}</span>'
            )
        n_ok = sum(r["correct"] for r in by_q[qid])
        p = prev_q.get(qid)
        if p is None:
            verdict = "🆕" if prev else ""
        else:
            p_frac, c_frac = p["correct"] / p["runs"], n_ok / len(by_q[qid])
            verdict = "🟢" if c_frac > p_frac else ("🔻" if c_frac < p_frac else "·")
        rows.append(
            f"<tr><td><code>{_esc(qid)}</code></td><td>{n_ok}/{len(by_q[qid])}</td>"
            f'<td class="runs">{"".join(cells)}</td><td>{verdict}</td></tr>'
        )
    prev_label = _esc(prev["tag"]) if prev else "—"
    return (
        f"<table><thead><tr><th>question</th><th>passed</th><th>runs (hover for grader reason, "
        f"↗ = trace)</th><th>vs {prev_label}</th></tr></thead><tbody>"
        + "".join(rows)
        + "</tbody></table>"
    )


def _failures(runs: list[dict]) -> str:
    failed = [r for r in runs if not r["correct"]]
    if not failed:
        return "<p>No failed runs. 🎉</p>"
    items = []
    for r in failed:
        reason = _esc(r.get("grader_reason") or ("crashed" if r.get("crashed") else "no reason recorded"))
        link = f' — <a href="{_esc(r["trace_url"])}">trace ↗</a>' if r.get("trace_url") else ""
        items.append(
            f"<details><summary><code>{_esc(r['id'])}</code> [{_esc(r['subject'])}] "
            f"{r.get('tokens', 0):,} tokens{link}</summary><p>{reason}</p></details>"
        )
    return "".join(items)


def build_html(
    *,
    record: dict,
    runs: list[dict],
    questions: dict[str, dict],
    history: list[dict],
    trend_png: Path,
) -> str:
    prev = history[-1] if history else None
    pcts = record["token_percentiles"]
    pct_row = "".join(f"<td>{pcts[k]:,.0f}</td>" for k in ("p10", "p50", "p90", "p99", "max"))
    return f"""<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>panda release qualification — {_esc(record["tag"])}</title>
<style>
  :root {{ color-scheme: light dark; }}
  body {{ font: 15px/1.5 ui-sans-serif, system-ui, sans-serif; max-width: 980px;
         margin: 2rem auto; padding: 0 1rem; }}
  h1 {{ font-size: 1.4rem; }} h2 {{ font-size: 1.1rem; margin-top: 2.2rem; }}
  table {{ border-collapse: collapse; width: 100%; font-size: .92em; }}
  th, td {{ text-align: left; padding: .35rem .6rem; border-bottom: 1px solid
            color-mix(in srgb, currentColor 18%, transparent); vertical-align: top; }}
  tr.current {{ background: color-mix(in srgb, currentColor 7%, transparent); }}
  .cards {{ display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
            gap: .8rem; margin: 1.2rem 0; }}
  .card {{ border: 1px solid color-mix(in srgb, currentColor 22%, transparent);
           border-radius: 8px; padding: .7rem .9rem; }}
  .card .k {{ font-size: .78em; opacity: .7; text-transform: uppercase;
              letter-spacing: .04em; }}
  .card .v {{ font-size: 1.5em; font-weight: 600; }}
  .card .s {{ font-size: .8em; opacity: .65; }}
  .run {{ display: inline-block; margin: 0 .45rem .2rem 0; cursor: default; }}
  .run.ok {{ color: #2da44e; }} .run.bad {{ color: #cf222e; font-weight: 700; }}
  .run .tok {{ font-size: .72em; opacity: .6; margin-left: .15rem; color: initial; }}
  img {{ max-width: 100%; height: auto; }}
  code {{ font-size: .92em; }}
  .meta, .note {{ font-size: .85em; opacity: .7; }}
  details {{ margin: .4rem 0; }} details p {{ margin: .4rem 0 .6rem 1.2rem; }}
</style></head><body>
<h1>🐼 Release qualification — {_esc(record["tag"])}</h1>
<p class="meta">commit <code>{_esc(record["commit"][:9])}</code> ·
{_esc(record["created_at"])} · cases <code>{_esc(record["cases"])}</code> ·
subject <code>{_esc(", ".join(record["subjects"]))}</code>
{"· pre-release" if record.get("prerelease") else ""}</p>
{_headline(record, questions)}
<h2>Releases compared</h2>
{_history_table(record, history)}
<h2>Trend</h2>
{_img(trend_png) or "<p class=note>no trend chart</p>"}
<h2>Per-question results</h2>
{_question_matrix(runs, prev)}
<h2>Categories</h2>
{_category_table(record.get("categories", []))}
<h2>Token distribution (correct runs)</h2>
<table><thead><tr><th>p10</th><th>p50</th><th>p90</th><th>p99</th><th>max</th></tr></thead>
<tbody><tr>{pct_row}</tbody></table>
<h2>Failed runs ({sum(1 for r in runs if not r["correct"])})</h2>
{_failures(runs)}
<p class="note">Single-pass run: small headline-score moves are noise — per-question flips
are the signal. History comes from prior releases' <code>eval-qualification.json</code>
assets.</p>
</body></html>
"""
