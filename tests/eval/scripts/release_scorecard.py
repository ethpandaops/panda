"""Release-qualification scorecard: turn one eval.json into a comparable release record.

    uv run python -m scripts.release_scorecard --eval-json reports/eval.json \
        --tag v0.32.0 --commit abc1234 --history-dir history --out-dir reports/release

Reads the JSON summary written by ``scripts.eval``, pools runs per question, and emits
three artifacts for the release-eval workflow:

  - ``eval-qualification.json`` — this release's record, uploaded as a release asset so
    future qualification runs can fetch it for comparison
  - ``eval-trend.png`` — pass-rate / score / token trend across qualified releases
  - ``scorecard.md`` — marker-delimited markdown the workflow splices into the GitHub
    release body

A single pass over the full cases file carries a few points of noise in the headline
numbers, so the scorecard leads with per-question flips against the previous qualified
release — which questions changed pass/fail status — and presents the aggregate scores
as a trend to eyeball, not a gate.

History is whatever ``--history-dir`` holds: one ``<tag>.json`` per previously qualified
release, downloaded from that release's assets. Releases that predate qualification have
no record and are silently absent.
"""

from __future__ import annotations

import argparse
import json
import statistics
from datetime import UTC, datetime
from pathlib import Path

SCHEMA = 1
MARKER_START = "<!-- eval-scorecard:start -->"
MARKER_END = "<!-- eval-scorecard:end -->"
TABLE_HISTORY = 8  # releases shown in the comparison table
CHART_HISTORY = 20  # releases plotted in the trend chart


def _parse_args() -> argparse.Namespace:
    ap = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    ap.add_argument("--eval-json", required=True, help="JSON summary written by scripts.eval")
    ap.add_argument("--tag", required=True, help="release tag being qualified (e.g. v0.32.0)")
    ap.add_argument("--commit", default="", help="commit SHA the release was built from")
    ap.add_argument(
        "--prerelease", action="store_true", help="mark the record as a pre-release run"
    )
    ap.add_argument(
        "--history-dir",
        default="",
        help="dir of <tag>.json qualification records from previous releases",
    )
    ap.add_argument("--out-dir", required=True, help="where to write the three artifacts")
    ap.add_argument(
        "--langfuse-links",
        default="",
        help="langfuse_links.md from scripts.eval (folded into the scorecard when present)",
    )
    ap.add_argument(
        "--repo", default="", help="owner/name, for the trend-chart asset URL in the markdown"
    )
    return ap.parse_args()


def _pool_questions(runs: list[dict]) -> dict[str, dict]:
    """Pool per-run records into per-question cells (variations share the question id)."""
    cells: dict[str, dict] = {}
    for run in runs:
        cell = cells.setdefault(
            run["id"], {"runs": 0, "correct": 0, "tokens_correct": [], "fail_reasons": []}
        )
        cell["runs"] += 1
        if run["correct"]:
            cell["correct"] += 1
            if run.get("tokens", 0) > 0:
                cell["tokens_correct"].append(run["tokens"])
        else:
            reason = "crashed" if run.get("crashed") else (run.get("grader_reason") or "")
            cell["fail_reasons"].append(" ".join(reason.split()))
    return {
        qid: {
            "runs": c["runs"],
            "correct": c["correct"],
            "mean_tokens_correct": round(statistics.mean(c["tokens_correct"]), 1)
            if c["tokens_correct"]
            else 0.0,
            "fail_reasons": c["fail_reasons"],
        }
        for qid, c in sorted(cells.items())
    }


def _build_record(args: argparse.Namespace, summary: dict, questions: dict[str, dict]) -> dict:
    correct_tokens = [
        r["tokens"] for r in summary["runs"] if r["correct"] and r.get("tokens", 0) > 0
    ]
    return {
        "schema": SCHEMA,
        "tag": args.tag,
        "commit": args.commit,
        "created_at": datetime.now(UTC).isoformat(timespec="seconds"),
        "prerelease": args.prerelease,
        "cases": summary.get("cases", ""),
        "subjects": summary.get("subjects", []),
        "runs": len(summary["runs"]),
        "pass_rate": round(summary["pass_rate"], 4),
        "mean_score": round(summary["mean_score"], 4),
        "mean_tokens_correct": round(statistics.mean(correct_tokens), 1)
        if correct_tokens
        else 0.0,
        # fail_reasons are scorecard detail, not part of the durable comparison record
        "questions": {
            qid: {k: v for k, v in cell.items() if k != "fail_reasons"}
            for qid, cell in questions.items()
        },
    }


def _load_history(history_dir: str) -> list[dict]:
    """Previous releases' records, oldest first. Unreadable files are skipped, not fatal."""
    if not history_dir:
        return []
    entries = []
    for path in Path(history_dir).glob("*.json"):
        try:
            entry = json.loads(path.read_text())
        except (OSError, json.JSONDecodeError) as exc:
            print(f"skipping unreadable history record {path}: {exc}")
            continue
        if entry.get("tag") and entry.get("created_at"):
            entries.append(entry)
    return sorted(entries, key=lambda e: e["created_at"])


def _render_trend(entries: list[dict], out: Path) -> None:
    import matplotlib

    matplotlib.use("Agg")
    import matplotlib.pyplot as plt

    tags = [e["tag"] for e in entries]
    xs = range(len(entries))
    fig, (ax_rate, ax_tok) = plt.subplots(2, 1, figsize=(9, 6), sharex=True, height_ratios=[2, 1])
    fig.suptitle("release qualification trend", fontsize=11)

    ax_rate.plot(xs, [e["pass_rate"] for e in entries], marker="o", color="tab:green",
                 label="pass rate")
    ax_rate.plot(xs, [e["mean_score"] for e in entries], marker="o", color="tab:blue",
                 label="mean score")
    ax_rate.set_ylim(-0.05, 1.05)
    ax_rate.legend(fontsize=8, loc="lower left")
    ax_rate.grid(alpha=0.25)

    ax_tok.plot(xs, [e["mean_tokens_correct"] for e in entries], marker="o", color="tab:orange")
    ax_tok.set_ylabel("mean tokens\n(correct runs)")
    ax_tok.grid(alpha=0.25)

    ax_tok.set_xticks(list(xs))
    ax_tok.set_xticklabels(tags, rotation=45, ha="right", fontsize=8)
    fig.tight_layout()
    fig.savefig(out, dpi=110)
    plt.close(fig)


def _question_flips(prev: dict, current: dict[str, dict]) -> list[str]:
    """Markdown rows for questions whose correct/runs fraction changed vs the previous
    record (including questions added or removed from the suite)."""
    rows = []
    prev_q = prev.get("questions", {})
    for qid in sorted(set(prev_q) | set(current)):
        p, c = prev_q.get(qid), current.get(qid)
        if p is None:
            rows.append(f"| `{qid}` | — | {c['correct']}/{c['runs']} | 🆕 new question |")
            continue
        if c is None:
            rows.append(f"| `{qid}` | {p['correct']}/{p['runs']} | — | removed |")
            continue
        p_frac = p["correct"] / p["runs"] if p["runs"] else 0.0
        c_frac = c["correct"] / c["runs"] if c["runs"] else 0.0
        if c_frac == p_frac:
            continue
        verdict = "🟢 improved" if c_frac > p_frac else "🔻 regressed"
        rows.append(
            f"| `{qid}` | {p['correct']}/{p['runs']} | {c['correct']}/{c['runs']} | {verdict} |"
        )
    return rows


def _fold_langfuse(path: str, lines: list[str]) -> None:
    if not path or not Path(path).exists():
        return
    content = Path(path).read_text().strip()
    # Drop the file's own "### 🔭 Langfuse traces" heading; the <details> summary replaces it.
    body = [ln for ln in content.splitlines() if not ln.startswith("#")]
    n_links = sum(1 for ln in body if ln.lstrip().startswith("- "))
    lines += [
        "",
        f"<details><summary>🔭 Langfuse traces ({n_links} runs; ⚠️ = failed)</summary>",
        "",
        *body,
        "",
        "</details>",
    ]


def _build_markdown(
    args: argparse.Namespace,
    record: dict,
    questions: dict[str, dict],
    history: list[dict],
) -> str:
    n_questions = len(questions)
    lines = [
        MARKER_START,
        "## 🐼 Release qualification",
        "",
        f"Full eval `{record['cases']}`: {n_questions} questions, {record['runs']} runs "
        f"(canonical + paraphrase variations, single pass) against the hosted proxy. "
        f"Subject `{', '.join(record['subjects'])}`, commit `{record['commit'][:7]}`.",
        "",
        "| release | pass rate | mean score | mean tokens (correct) |",
        "|---|---|---|---|",
    ]

    def row(e: dict, bold: bool = False) -> str:
        tag = f"**{e['tag']} (this release)**" if bold else e["tag"]
        n_correct = round(e["pass_rate"] * e["runs"])
        return (
            f"| {tag} | {e['pass_rate']:.0%} ({n_correct}/{e['runs']}) "
            f"| {e['mean_score']:.3f} | {e['mean_tokens_correct']:,.0f} |"
        )

    lines.append(row(record, bold=True))
    lines += [row(e) for e in reversed(history[-TABLE_HISTORY:])]

    if history:
        prev = history[-1]
        flips = _question_flips(prev, questions)
        lines += ["", f"### Per-question changes vs {prev['tag']}", ""]
        if flips:
            lines += [f"| question | {prev['tag']} | {record['tag']} | |", "|---|---|---|---|"]
            lines += flips
        else:
            lines.append(f"No per-question changes vs {prev['tag']}.")
    else:
        lines += ["", "_First qualified release — no prior records to compare against._"]

    failed = {qid: c for qid, c in questions.items() if c["correct"] < c["runs"]}
    if failed:
        lines += [
            "",
            "### Failed runs",
            "",
            "| question | failed | sample grader reason |",
            "|---|---|---|",
        ]
        for qid, c in failed.items():
            reason = (c["fail_reasons"][0] or "no reason recorded")[:140]
            lines.append(f"| `{qid}` | {c['runs'] - c['correct']}/{c['runs']} | {reason} |")

    if args.repo:
        lines += [
            "",
            f"![eval trend](https://github.com/{args.repo}/releases/download/"
            f"{record['tag']}/eval-trend.png)",
        ]

    _fold_langfuse(args.langfuse_links, lines)

    lines += [
        "",
        "<sub>Single-pass run: small headline-score moves are noise — the per-question "
        "flips are the signal. History comes from prior releases' "
        "<code>eval-qualification.json</code> assets.</sub>",
        MARKER_END,
        "",
    ]
    return "\n".join(lines)


def main() -> None:
    args = _parse_args()
    summary = json.loads(Path(args.eval_json).read_text())
    questions = _pool_questions(summary["runs"])
    record = _build_record(args, summary, questions)
    history = _load_history(args.history_dir)

    out_dir = Path(args.out_dir)
    out_dir.mkdir(parents=True, exist_ok=True)
    (out_dir / "eval-qualification.json").write_text(json.dumps(record, indent=2) + "\n")
    _render_trend(history[-CHART_HISTORY:] + [record], out_dir / "eval-trend.png")
    (out_dir / "scorecard.md").write_text(_build_markdown(args, record, questions, history))

    print(
        f"qualified {record['tag']}: pass-rate {record['pass_rate']:.0%} "
        f"mean-score {record['mean_score']:.3f} over {record['runs']} runs "
        f"({len(history)} prior release(s) for comparison) -> {out_dir}"
    )


if __name__ == "__main__":
    main()
