"""CI smoke report: turn one smoke eval.json into the same interactive report a
release gets, anchored to where this commit sits.

    uv run python -m scripts.ci_report --eval-json reports/eval.json \
        --branch feat/foo --sha abc123... --history-dir history \
        --baseline baselines/master.json --baseline baselines/release.json \
        --out-dir reports/ci

The record is identified by branch + commit instead of a tag. Its comparison
history is assembled from three sources, all optional:

  - ``--history-dir`` — this branch's previous smoke payloads (from gh-pages), so
    the trend chart walks the branch's commits
  - ``--baseline`` (repeatable) — reference records: the latest master smoke run
    and the most recent release's eval-qualification.json. Release records cover
    the full suite, so they are restricted to the questions this run actually
    asked before they enter the comparison (pass rate and per-question cells are
    recomputed from the record's question map; mean score is not recomputable
    from a record and is nulled — the page renders a dash).

Emits into ``--out-dir``:

  - ``data.json`` — the report payload (record + enriched runs + history), what
    ``scripts.ci_pages publish`` puts on gh-pages for the fetch-mode viewer
  - ``eval-report.html`` — the same payload baked into a self-contained page, so
    the CI artifact is inspectable offline (fork PRs never publish)
  - ``pr_comment.md`` — the single sticky PR comment: headline + viewer link (when
    ``--pages-url`` is given), per-question results, Langfuse trace links
"""

from __future__ import annotations

import argparse
import json
import re
import statistics
from pathlib import Path

from scripts.release_report import build_payload, render_template
from scripts.release_scorecard import _build_record, _fold_langfuse, _pool_questions

MAX_BRANCH_HISTORY = 30


def sanitize_branch(branch: str) -> str:
    """Branch name -> filesystem/URL-safe path segment."""
    safe = re.sub(r"[^A-Za-z0-9._-]", "-", branch)[:100].strip(".")
    return safe or "branch"


def _parse_args() -> argparse.Namespace:
    ap = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    ap.add_argument("--eval-json", required=True, help="JSON summary written by scripts.eval")
    ap.add_argument("--branch", required=True, help="branch this run measured")
    ap.add_argument("--sha", required=True, help="commit SHA this run measured")
    ap.add_argument("--event", default="", help="triggering event (push / pull_request / ...)")
    ap.add_argument("--run-url", default="", help="CI run URL, linked from the page")
    ap.add_argument("--pr", default="", help="PR number, when triggered by one")
    ap.add_argument("--repo", default="", help="owner/name, for commit links on the page")
    ap.add_argument("--commit-message", default="", help="subject line of the measured commit")
    ap.add_argument(
        "--history-dir",
        default="",
        help="dir of this branch's previous smoke payloads or records",
    )
    ap.add_argument(
        "--baseline",
        action="append",
        default=[],
        help="reference record/payload JSON (master baseline, latest release); repeatable",
    )
    ap.add_argument("--out-dir", required=True, help="where to write the artifacts")
    ap.add_argument(
        "--pages-url",
        default="",
        help="public URL of the gh-pages CI viewer; adds the report link to pr_comment.md",
    )
    ap.add_argument(
        "--langfuse-links",
        default="",
        help="langfuse_links.md from scripts.eval (folded into pr_comment.md when present)",
    )
    return ap.parse_args()


def _load_record(path: Path) -> dict | None:
    """Read a comparison record from either a viewer payload ({record, runs, ...})
    or a bare record file. None (skipped) on anything unreadable — a smoke report
    must not die over a malformed baseline."""
    try:
        data = json.loads(path.read_text())
    except (OSError, json.JSONDecodeError) as exc:
        print(f"skipping unreadable comparison record {path}: {exc}")
        return None
    record = data.get("record", data) if isinstance(data, dict) else None
    if not isinstance(record, dict) or not record.get("created_at"):
        print(f"skipping {path}: no record with created_at")
        return None
    return record


def restrict_record(record: dict, qids: set[str]) -> dict | None:
    """Reduce a full-suite record to the questions this run asked, so a release
    baseline is comparable to a smoke run. Aggregates are recomputed from the
    record's per-question cells; mean score cannot be (it needs per-run data) and
    is nulled. None when the record shares no questions with this run."""
    questions = {qid: cell for qid, cell in (record.get("questions") or {}).items() if qid in qids}
    n_runs = sum(c["runs"] for c in questions.values())
    if not n_runs:
        return None
    n_correct = sum(c["correct"] for c in questions.values())
    refs = [
        c.get("median_tokens_correct") or c.get("mean_tokens_correct") or 0.0
        for c in questions.values()
        if c["correct"]
    ]
    refs = [r for r in refs if r > 0]
    return {
        "schema": record.get("schema"),
        "kind": record.get("kind", "release"),
        "tag": record.get("tag", "?"),
        "commit": record.get("commit", ""),
        "created_at": record["created_at"],
        "prerelease": record.get("prerelease", False),
        "cases": f"{record.get('cases', '')} → {len(questions)} shared questions",
        "subjects": record.get("subjects", []),
        "runs": n_runs,
        "pass_rate": round(n_correct / n_runs, 4),
        "mean_score": None,
        # Closest stand-in for tokens-p50 derivable from a record: the median of
        # the shared questions' own median costs. The page marks it approximate.
        "mean_tokens_correct": round(statistics.median(refs), 1) if refs else 0.0,
        "restricted": True,
        "questions": questions,
    }


def _assemble_history(args: argparse.Namespace, qids: set[str]) -> list[dict]:
    entries: list[dict] = []
    if args.history_dir:
        for path in sorted(Path(args.history_dir).glob("*.json")):
            record = _load_record(path)
            if record and record.get("commit") != args.sha:
                entries.append(record)
    # Every payload embeds its comparison history, so an uncapped branch would make
    # payload size grow with branch age. The chart only usefully shows ~30 points;
    # baselines are appended after the cap so they always survive it.
    entries = sorted(entries, key=lambda e: e["created_at"])[-MAX_BRANCH_HISTORY:]
    for raw in args.baseline:
        path = Path(raw)
        if not path.exists():
            print(f"baseline {path} absent; skipping")
            continue
        record = _load_record(path)
        if not record:
            continue
        # CI records already ran exactly these questions; full-suite (release)
        # records need restricting or the per-question diff drowns in "removed" rows.
        if record.get("kind") != "ci":
            record = restrict_record(record, qids)
            if not record:
                print(f"baseline {path} shares no questions with this run; skipping")
                continue
        if record.get("commit") != args.sha:
            entries.append(record)
    return sorted(entries, key=lambda e: e["created_at"])


def _build_comment(
    args: argparse.Namespace, record: dict, summary: dict, history: list[dict]
) -> str:
    """The single sticky PR comment: headline + report link, per-question results,
    and the Langfuse trace links folded into a collapsible — one comment instead of
    a results comment, a traces comment, and a link comment."""
    n_correct = sum(1 for r in summary["runs"] if r["correct"])
    short = args.sha[:7]
    status = "✅" if n_correct == record["runs"] else "❌"
    lines = [f"### 🐼 Smoke eval — `{short}`: {status} {n_correct}/{record['runs']} pass", ""]

    headline = (
        f"tokens p50 {record['token_percentiles']['p50']:,.0f} · "
        f"tokens/solve {record['tokens_per_solve']:,.0f}"
    )
    if args.pages_url:
        run_id = f"{sanitize_branch(args.branch)}/{args.sha}"
        url = f"{args.pages_url.rstrip('/')}/?run={run_id}"
        lines.append(f"**[📊 Interactive report]({url})** — {headline}.")
    else:
        lines.append(f"{headline} · report not published for this run (see the artifact).")

    anchors = " · ".join(f"`{e['tag']}` {e['pass_rate']:.0%}" for e in history[-3:])
    if anchors:
        lines += ["", f"Reference points: {anchors}."]

    lines += ["", "| question | result | tokens | tools |", "|---|---|---|---|"]
    for run in summary["runs"]:
        mark = "💥 crash" if run.get("crashed") else ("✅" if run["correct"] else "❌")
        lines.append(
            f"| `{run['id']}` | {mark} | {run.get('tokens', 0):,} | {run.get('tools', '—')} |"
        )

    _fold_langfuse(args.langfuse_links, lines)

    lines += [
        "",
        "<sub>The report walks this branch's commits against the master baseline and "
        "the most recent release. A self-contained copy is in the run's "
        "<code>eval-smoke-*</code> artifact.</sub>",
        "",
    ]
    return "\n".join(lines)


def main() -> None:
    args = _parse_args()
    summary = json.loads(Path(args.eval_json).read_text())
    questions = _pool_questions(summary["runs"])
    history = _assemble_history(args, set(questions))

    short = args.sha[:7]
    record_args = argparse.Namespace(
        tag=f"{args.branch}@{short}", commit=args.sha, prerelease=False
    )
    record = _build_record(record_args, summary, questions, history[-1] if history else None)
    record.update(
        kind="ci",
        branch=args.branch,
        commit_message=args.commit_message[:200],
        event=args.event,
        run_url=args.run_url,
        repo=args.repo,
        pr=args.pr,
    )

    payload = build_payload(record=record, runs=summary["runs"], history=history)
    out_dir = Path(args.out_dir)
    out_dir.mkdir(parents=True, exist_ok=True)
    (out_dir / "data.json").write_text(json.dumps(payload, separators=(",", ":")) + "\n")
    (out_dir / "eval-report.html").write_text(render_template(payload))
    (out_dir / "pr_comment.md").write_text(_build_comment(args, record, summary, history))

    print(
        f"ci report for {record['tag']}: pass-rate {record['pass_rate']:.0%} over "
        f"{record['runs']} runs ({len(history)} comparison record(s)) -> {out_dir}"
    )


if __name__ == "__main__":
    main()
