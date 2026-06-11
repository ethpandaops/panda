"""Unified single-pass eval: run cases through promptfoo, report, exit nonzero on failure.

    uv run python -m scripts.eval --tags smoke
    uv run python -m scripts.eval                      # every case in cases/*.yaml
    uv run python -m scripts.eval --tags mev,blobs --subject opencode-go/deepseek-v4-flash:cli

Runs each case (single- or multi-turn) against the agent subject(s) via promptfoo, grades
with the case's ``assert:`` blocks, prints a table, and writes JUnit XML (``--junit``) so CI
can publish a check + PR comment. Exit code is nonzero if any case fails.

This is the measure-once entry point. ``scripts.harden`` wraps this SAME measurement core
(``harden.promptfoo_eval.measure_candidate``) in an optimization loop — same harness, the
loop is just different launch params.

Needs a panda server the agent can reach. In CI a server is already running; for local use
``--scratch`` builds + runs one from the candidate source on :2481 (like harden does).
Langfuse falls out for free: the promptfoo worker inherits LANGFUSE_* from the environment,
so when keys are set every run is pushed to Langfuse by the agent itself.
"""

from __future__ import annotations

import argparse
import asyncio
import json
import os
import sys
import time
from pathlib import Path
from xml.etree import ElementTree as ET

from rich.console import Console
from rich.table import Table

from cases.loader import load_test_cases
from config.settings import DEFAULT_EVALUATOR_MODEL, DEFAULT_SUBJECTS
from harden.logsetup import setup_logging
from harden.promptfoo_eval import measure_candidate, scrub_secrets
from harden.runner import CandidateResult, Question

console = Console()


def _parse_args() -> argparse.Namespace:
    ap = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    ap.add_argument(
        "--cases", default="", help="restrict to one cases/*.yaml file (default: all files)"
    )
    ap.add_argument(
        "--tags",
        action="append",
        default=[],
        help="run only cases carrying at least one of these tags (repeatable, comma-ok); "
        "e.g. --tags smoke",
    )
    ap.add_argument(
        "--exclude-tags",
        action="append",
        default=[],
        help="drop cases carrying any of these tags (repeatable, comma-ok)",
    )
    ap.add_argument(
        "--subject", action="append", default=[], help="provider/model:route (repeatable)"
    )
    ap.add_argument("--question-id", action="append", default=[], help="restrict to case id(s)")
    ap.add_argument(
        "--no-variations",
        action="store_true",
        help="run only each case's canonical phrasing (CI smoke: fast, cheap)",
    )
    ap.add_argument("-k", "--repeat", type=int, default=1, help="runs per (case, subject)")
    ap.add_argument("--judge-model", default=DEFAULT_EVALUATOR_MODEL)
    ap.add_argument(
        "--grader", default="", help="promptfoo grading provider (default openrouter:<judge-model>)"
    )
    ap.add_argument("--concurrency", type=int, default=16, help="max agent runs in flight")
    ap.add_argument(
        "--subject-timeout",
        type=float,
        default=300.0,
        help="per-question agent timeout (seconds; CI-friendly default — harden uses "
        "a much longer one so slow runs are measured, not crashed)",
    )
    ap.add_argument(
        "--min-pass", type=float, default=1.0, help="min pass-rate to exit 0 (default 1.0 = all)"
    )
    ap.add_argument(
        "--per-question",
        action="store_true",
        help="gate on (question, subject) cells instead of runs: a cell passes if ANY of "
        "its repeats passed. Smoke semantics — 'can the pipeline answer this at all' — "
        "tolerant of single-run agent flubs; use with -k 2+.",
    )
    ap.add_argument("--junit", default="", help="write JUnit XML here (for CI)")
    ap.add_argument("--json", dest="json_out", default="", help="write a JSON summary here")
    ap.add_argument("--save-dir", default="", help="where to write run artifacts + traces")
    ap.add_argument(
        "--scratch", action="store_true", help="build + run a local scratch server from the source"
    )
    ap.add_argument("--port", type=int, default=2481, help="scratch server port (with --scratch)")
    ap.add_argument(
        "--sandbox",
        action="store_true",
        help="run the subject in a container with no repo access (can't read the eval cases); "
        "implies --scratch",
    )
    return ap.parse_args()


def _report(result: CandidateResult) -> None:
    table = Table(title="eval results", show_lines=False)
    for col in ("case", "subject", "ok", "score", "tokens", "tools", "answer"):
        table.add_column(col, overflow="fold")
    for rec in sorted(result.records, key=lambda r: (r.score.correct, r.score.score)):
        rs, tr = rec.score, rec.trace
        answer = "CRASHED: " + (tr.error or "") if tr.crashed else " ".join((tr.output or "").split())
        table.add_row(
            rec.question.id,
            rs.subject,
            "[green]✓[/green]" if rs.correct else "[red]✗[/red]",
            f"{rs.score:.2f}",
            str(rs.tokens),
            str(rs.n_tools),
            answer[:90],
        )
    console.print(table)
    console.print(
        f"pass-rate [bold]{result.pass_rate:.0%}[/bold]  mean-score [bold]{result.score:.3f}[/bold]  "
        f"({len(result.records)} runs)"
    )


def _write_junit(path: str, result: CandidateResult, *, suite: str) -> None:
    records = result.records
    failures = sum(1 for r in records if not r.score.correct)
    ts = ET.Element(
        "testsuite", name=suite, tests=str(len(records)), failures=str(failures), errors="0"
    )
    seen: dict[tuple[str, str], int] = {}
    for rec in records:
        rs, tr = rec.score, rec.trace
        key = (rs.question_id, rs.subject)
        i = seen.get(key, 0)
        seen[key] = i + 1
        name = rs.question_id if i == 0 else f"{rs.question_id}#{i}"
        tc = ET.SubElement(
            ts, "testcase", classname=rs.subject, name=name, time=f"{tr.duration_ms / 1000:.1f}"
        )
        if not rs.correct:
            if tr.crashed:
                msg = f"crashed: {tr.error}"
            else:
                msg = f"failed grading (score={rs.score:.2f}): {rs.reason or 'no reason given'}"
            # The failure body carries raw agent output into an uploaded artifact and
            # the PR results comment — scrub credential values like the trace files do.
            fail = ET.SubElement(tc, "failure", message=scrub_secrets(msg[:400]))
            fail.text = scrub_secrets(f"grader reason: {rs.reason}\n\n{(tr.output or '')[:2000]}")
    p = Path(path)
    p.parent.mkdir(parents=True, exist_ok=True)
    ET.ElementTree(ts).write(str(p), encoding="utf-8", xml_declaration=True)
    console.print(f"[dim]wrote JUnit XML: {p}[/dim]")


def _write_json(path: str, result: CandidateResult, *, cases: str, subjects: list[str]) -> None:
    payload = {
        "cases": cases,
        "subjects": subjects,
        "pass_rate": result.pass_rate,
        "mean_score": result.score,
        "by_subject": result.by_subject,
        "runs": [
            {
                "id": r.score.question_id,
                "subject": r.score.subject,
                "correct": r.score.correct,
                "correctness": r.score.correctness,
                "score": r.score.score,
                "grader_reason": r.score.reason,
                "tokens": r.score.tokens,
                "tools": r.score.n_tools,
                "crashed": r.trace.crashed,
                "trace_id": r.trace.trace_id,
                "trace_url": r.trace.trace_url,
            }
            for r in result.records
        ],
    }
    p = Path(path)
    p.parent.mkdir(parents=True, exist_ok=True)
    p.write_text(json.dumps(payload, indent=2))
    console.print(f"[dim]wrote JSON summary: {p}[/dim]")


def _write_langfuse_links(path: Path, result: CandidateResult) -> None:
    """Write a markdown list of Langfuse trace deep-links (one per run) for a PR comment.
    No-op when Langfuse is disabled (no trace_urls). Marks failed runs so they're easy to
    open."""
    links = [
        (r.score.question_id, r.trace.trace_url, r.score.correct)
        for r in result.records
        if r.trace.trace_url
    ]
    if not links:
        return
    lines = ["### 🔭 Langfuse traces", ""]
    for qid, url, ok in links:
        mark = "" if ok else " ⚠️"
        lines.append(f"- [`{qid}`{mark}]({url})")
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text("\n".join(lines) + "\n")
    console.print(f"[dim]wrote Langfuse links: {path} ({len(links)} traces)[/dim]")


def _split_tags(values: list[str]) -> list[str]:
    return [t for v in values for t in v.split(",") if t.strip()]


def main() -> None:
    args = _parse_args()
    setup_logging()  # configure the rich logger so build/promptfoo sub-loggers render

    tags, exclude_tags = _split_tags(args.tags), _split_tags(args.exclude_tags)
    selection = args.cases or "all files"
    if tags:
        selection += f" tags={','.join(tags)}"
    if exclude_tags:
        selection += f" exclude={','.join(exclude_tags)}"

    questions = [
        Question(
            id=c.id,
            text=c.input,
            followups=c.followups,
            asserts=c.asserts,
            variations=[] if args.no_variations else c.variations,
        )
        for c in load_test_cases(args.cases or None, tags=tags, exclude_tags=exclude_tags)
    ]
    if args.question_id:
        wanted = set(args.question_id)
        questions = [q for q in questions if q.id in wanted]
    if not questions:
        raise SystemExit(f"no cases matched ({selection})")

    subject_specs = args.subject or DEFAULT_SUBJECTS
    grader = args.grader or f"openrouter:{args.judge_model}"
    os.environ.setdefault("PROMPTFOO_PYTHON", sys.executable)
    eval_dir = str(Path(__file__).resolve().parents[1])

    if args.sandbox:
        args.scratch = True  # the sandboxed subject needs the scratch server

    save_dir = args.save_dir or str(
        Path.home() / ".panda" / "harden" / "runs" / f"eval-{time.strftime('%Y-%m-%dT%H-%M-%S')}"
    )
    nvar = sum(len(q.variations) for q in questions)
    console.print(
        f"[bold]=== eval config ===[/bold]\n"
        f"  subjects (agent): {', '.join(subject_specs)}"
        + ("   [sandboxed: container, no repo access]" if args.sandbox else "   [host process]")
        + f"\n  grader:           {grader}"
        f"\n  cases:            {selection} | {len(questions)} question(s) + {nvar} variations "
        f"| k={args.repeat}"
        f"\n  artifacts:        {save_dir}\n[bold]===================[/bold]"
    )

    server = None
    if args.scratch:
        import subprocess

        from scripts._panda_env import (
            ScratchServer,
            make_apply,
            point_cli_at_scratch,
            prepare_opencode_sandbox,
            write_scratch_config,
        )

        repo_dir = subprocess.run(
            ["git", "rev-parse", "--show-toplevel"], text=True, capture_output=True, check=True
        ).stdout.strip()
        config_path = write_scratch_config(args.port)
        point_cli_at_scratch(repo_dir, config_path)
        server = ScratchServer(repo_dir, config_path, args.port)
        if args.sandbox:
            console.print("[dim]preparing opencode sandbox (image + linux panda)...[/dim]")
            prepare_opencode_sandbox(repo_dir, args.port)
        console.print("[dim]building + starting scratch server...[/dim]")
        make_apply(server, sandbox=args.sandbox)()

    try:
        result = asyncio.run(
            measure_candidate(
                questions,
                subject_specs,
                k=args.repeat,
                run_dir=save_dir,
                grader=grader,
                concurrency=args.concurrency,
                subject_timeout=int(args.subject_timeout),
                cwd=eval_dir,
            )
        )
    finally:
        if server is not None:
            server.stop()  # purges all sandbox sessions before terminating

    _report(result)
    gate_pass = result.pass_rate
    if args.per_question:
        cells: dict[tuple[str, str], bool] = {}
        for r in result.records:
            key = (r.score.question_id, r.score.subject)
            cells[key] = cells.get(key, False) or r.score.correct
        gate_pass = sum(cells.values()) / len(cells) if cells else 0.0
        failed = sorted(f"{q} [{s}]" for (q, s), ok in cells.items() if not ok)
        console.print(
            f"per-question gate: [bold]{gate_pass:.0%}[/bold] of {len(cells)} cells"
            + (f" — failed: {', '.join(failed)}" if failed else "")
        )
    if args.junit:
        suite = Path(args.cases).stem if args.cases else (",".join(tags) or "all")
        _write_junit(args.junit, result, suite=suite)
    if args.json_out:
        _write_json(args.json_out, result, cases=selection, subjects=subject_specs)
    # Langfuse links go next to the JUnit/JSON (the reports dir CI uploads + comments from).
    reports_dir = Path(args.junit).parent if args.junit else (
        Path(args.json_out).parent if args.json_out else Path("reports")
    )
    _write_langfuse_links(reports_dir / "langfuse_links.md", result)

    sys.exit(0 if gate_pass >= args.min_pass else 1)


if __name__ == "__main__":
    main()
