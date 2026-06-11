"""Interactive single-file HTML report for a release-qualification run.

``build_html`` no longer renders HTML in Python: it builds a JSON payload — the
release record, every run enriched with its case tags, and the cross-release
history — and injects it into ``report_template.html``, a self-contained
vanilla-JS exploration app (hand-rolled SVG charts, filterable run explorer,
per-question matrix, full grader reasons). The output makes zero external
fetches, so it works as a release asset, a CI artifact, or a GitHub Pages page,
offline.

``token_percentiles`` and ``category_breakdown`` live here and are imported by
``scripts.release_scorecard`` to build the durable qualification record.
"""

from __future__ import annotations

import json
import statistics
from pathlib import Path

_TEMPLATE = Path(__file__).with_name("report_template.html")
_PLACEHOLDER = "/*__PANDA_EVAL_DATA__*/"


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


def _case_meta(cases_file: str) -> dict[str, dict]:
    """Question-id -> {tags, input} from the cases file. Empty on any failure — the
    suite may have changed since this record's run, and a report must not die over
    case metadata."""
    try:
        from cases.loader import load_test_cases
        from scripts.release_scorecard import _load_case_set

        cases = _load_case_set(load_test_cases, cases_file)
        return {c.id: {"tags": list(c.tags or []), "input": c.input} for c in cases}
    except Exception as exc:
        print(f"no case metadata for report ({exc}); runs will carry empty tags")
        return {}


def build_html(
    *,
    record: dict,
    runs: list[dict],
    questions: dict[str, dict],
    history: list[dict],
    trend_png: Path,
) -> str:
    """Inject the qualification payload into the report template.

    ``questions`` and ``trend_png`` are accepted for caller compatibility; the page
    derives per-question cells from the raw runs and draws every chart from data.
    """
    del questions, trend_png
    meta = _case_meta(record.get("cases", ""))
    enriched = []
    for run in runs:
        m = meta.get(run["id"], {})
        out = dict(run, tags=m.get("tags", []))
        # Older eval.json files predate per-run prompt capture: fall back to the
        # case's canonical wording and say so (paraphrase runs differ from it).
        if not out.get("question"):
            out["question"] = m.get("input", "")
            out["question_canonical"] = True
        enriched.append(out)
    payload = {"record": record, "runs": enriched, "history": history}
    # ``<\/`` is a valid JSON string escape, and breaking up ``</`` guarantees the
    # blob can never close the surrounding <script> tag (or open an HTML comment).
    blob = json.dumps(payload, separators=(",", ":")).replace("</", "<\\/")
    template = _TEMPLATE.read_text()
    if _PLACEHOLDER not in template:
        raise ValueError(f"placeholder {_PLACEHOLDER!r} missing from {_TEMPLATE}")
    return template.replace(_PLACEHOLDER, f"window.__PANDA_EVAL_DATA__ = {blob};", 1)
