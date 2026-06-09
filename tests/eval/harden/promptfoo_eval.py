"""Measurement via promptfoo — the test-runner the harden loop wraps.

promptfoo owns running the cases (across our agentic subjects, K times, concurrently) and
grading them with its asserts (llm-rubric / python / ...). This module generates a config
from a question set + subject specs, invokes ``promptfoo eval``, and parses the results
back into per-run results the loop can score and gate on. The FULL raw trace from each run
is written to ``run_dir/traces/`` so the proposer can read what it needs on demand — we do
NOT dump it into anyone's context here.
"""

from __future__ import annotations

import asyncio
import json
import statistics
import subprocess
from dataclasses import dataclass
from pathlib import Path

from config.settings import DEFAULT_AGENT_ROUTE, DEFAULT_GRADER
from harden.runner import CandidateResult, Question, RunRecord
from harden.scoring import candidate_score, pass_rate, score_run
from harden.trace import RunTrace, ToolCall

_PROVIDER = str(Path(__file__).resolve().parents[1] / "promptfoo" / "provider.py")

_DEFAULT_ASSERT = {
    "type": "llm-rubric",
    "value": "The response should be a plausible, complete answer to the user's question, "
    "grounded in real data the agent actually queried (not made up).",
}


@dataclass
class PfRun:
    """One graded run out of promptfoo: the raw trace + promptfoo's correctness verdict."""

    question_id: str
    subject: str
    trace: RunTrace
    correct: bool
    correctness: float


def build_config(
    questions: list[Question],
    subject_specs: list[str],
    *,
    grader: str,
    worker_timeout_ms: int,
    subject_timeout: int,
) -> dict:
    """A promptfoo config: providers = subjects, tests = questions with their asserts."""
    providers = []
    for spec in subject_specs:
        model, _, route = spec.partition(":")
        providers.append(
            {
                "id": f"file://{_PROVIDER}",
                "label": spec,
                "config": {
                    "model": model,
                    "route": route or DEFAULT_AGENT_ROUTE,
                    "timeout": worker_timeout_ms,  # promptfoo worker timeout (ms)
                    "subject_timeout": subject_timeout,  # our subject timeout (s)
                },
            }
        )
    # followups is JSON-encoded, NOT a raw list: promptfoo expands an array-valued var into
    # a test matrix (one case per element), which would split a multi-turn question into
    # bogus single-followup runs. A string var is passed through untouched; the provider
    # decodes it back into the turn list.
    tests = [
        {
            "vars": {"question": q.text, "followups": json.dumps(q.followups), "qid": q.id},
            "assert": q.asserts or [_DEFAULT_ASSERT],
        }
        for q in questions
    ]
    return {
        "description": "harden measurement",
        "prompts": ["{{question}}"],
        "providers": providers,
        "defaultTest": {"options": {"provider": grader}},
        "tests": tests,
    }


async def measure(
    questions: list[Question],
    subject_specs: list[str],
    *,
    k: int,
    run_dir: str,
    grader: str = DEFAULT_GRADER,
    concurrency: int = 6,
    worker_timeout_ms: int = 600000,
    subject_timeout: int = 300,
    cwd: str | None = None,
) -> list[PfRun]:
    """Run the cases × subjects × K through promptfoo and parse back graded runs."""
    rd = Path(run_dir)
    rd.mkdir(parents=True, exist_ok=True)
    cfg_path = rd / "promptfooconfig.json"
    results_path = rd / "pf_results.json"
    cfg_path.write_text(
        json.dumps(
            build_config(
                questions,
                subject_specs,
                grader=grader,
                worker_timeout_ms=worker_timeout_ms,
                subject_timeout=subject_timeout,
            ),
            indent=2,
        )
    )
    cmd = [
        "npx", "promptfoo@latest", "eval",
        "-c", str(cfg_path),
        "-o", str(results_path),
        "--no-cache",
        "-j", str(concurrency),
        "--repeat", str(k),
    ]

    def run() -> subprocess.CompletedProcess:
        return subprocess.run(cmd, cwd=cwd, capture_output=True, text=True)

    proc = await asyncio.to_thread(run)
    if not results_path.exists():
        raise RuntimeError(
            f"promptfoo produced no results:\n{(proc.stderr or proc.stdout or '')[-1500:]}"
        )
    return _parse(results_path, rd)


def score_runs(
    pf_runs: list[PfRun], questions: list[Question], *, budget: int, steepness: float = 2.0
) -> CandidateResult:
    """Turn promptfoo's graded runs into the loop's CandidateResult (the gates/objective)."""
    by_id = {q.id: q for q in questions}
    runs, records, by_subject = [], [], {}
    for pf in pf_runs:
        rs = score_run(
            pf.trace,
            correct=pf.correct,
            correctness=pf.correctness,
            budget=budget,
            steepness=steepness,
            question_id=pf.question_id,
        )
        runs.append(rs)
        question = by_id.get(pf.question_id) or Question(id=pf.question_id, text=pf.trace.question)
        records.append(RunRecord(question=question, trace=pf.trace, score=rs))
        by_subject.setdefault(pf.subject, []).append(rs.score)
    return CandidateResult(
        runs=runs,
        records=records,
        score=candidate_score(runs),
        pass_rate=pass_rate(runs),
        by_subject={n: statistics.mean(s) for n, s in by_subject.items() if s},
    )


async def measure_candidate(
    questions: list[Question],
    subject_specs: list[str],
    *,
    k: int,
    budget: int,
    run_dir: str,
    steepness: float = 2.0,
    grader: str = DEFAULT_GRADER,
    concurrency: int = 6,
    subject_timeout: int = 300,
    cwd: str | None = None,
) -> CandidateResult:
    """Measure one harness state via promptfoo and score it — what the loop calls."""
    pf_runs = await measure(
        questions,
        subject_specs,
        k=k,
        run_dir=run_dir,
        grader=grader,
        concurrency=concurrency,
        subject_timeout=subject_timeout,
        cwd=cwd,
    )
    return score_runs(pf_runs, questions, budget=budget, steepness=steepness)


def _subject_label(provider) -> str:
    if isinstance(provider, dict):
        return provider.get("label") or provider.get("id") or ""
    return str(provider)


def _parse(results_path: Path, run_dir: Path) -> list[PfRun]:
    data = json.loads(results_path.read_text())
    traces_dir = run_dir / "traces"
    traces_dir.mkdir(exist_ok=True)
    counters: dict[tuple[str, str], int] = {}
    runs: list[PfRun] = []
    for r in data.get("results", {}).get("results", []):
        vars_ = r.get("vars") or {}
        qid = vars_.get("qid", "")
        subject = _subject_label(r.get("provider"))
        resp = r.get("response") or {}
        md = resp.get("metadata") or {}
        # `answer` is the clean answer; `response.output` is answer + tool appendix (what the
        # grader judged). Store the clean one for reporting; the tools live in tool_calls.
        trace = RunTrace(
            question=vars_.get("question", ""),
            subject=md.get("subject", subject),
            output=md.get("answer", resp.get("output", "")) or "",
            tool_calls=[
                ToolCall(
                    name=t.get("name", ""),
                    arguments=t.get("arguments", "") or "",
                    output=t.get("output", "") or "",
                    is_error=bool(t.get("is_error")),
                    duration_ms=int(t.get("duration_ms") or 0),
                )
                for t in (md.get("tool_calls") or [])
            ],
            input_tokens=int(md.get("input_tokens") or 0),
            output_tokens=int(md.get("output_tokens") or 0),
            duration_ms=int(md.get("duration_ms") or 0),
            crashed=bool(md.get("crashed")),
            error=md.get("error"),
            trace_id=md.get("trace_id"),
            trace_url=md.get("trace_url"),
        )
        grading = r.get("gradingResult") or {}
        correct = bool(r.get("success"))
        score = grading.get("score")
        correctness = float(score if score is not None else (1.0 if correct else 0.0))

        i = counters.get((qid, subject), 0)
        counters[(qid, subject)] = i + 1
        safe = subject.replace("/", "-").replace(":", "-")
        _write_trace_file(traces_dir / f"{qid}__{safe}__{i}.txt", trace, correct, correctness)
        runs.append(PfRun(qid, subject, trace, correct, correctness))
    return runs


def _write_trace_file(path: Path, trace: RunTrace, correct: bool, correctness: float) -> None:
    lines = [
        f"question: {trace.question}",
        f"subject={trace.subject} correct={correct} correctness={correctness:.2f} "
        f"tokens={trace.total_tokens} tools={trace.n_tools} crashed={trace.crashed}",
        "",
    ]
    for i, tc in enumerate(trace.tool_calls, 1):
        err = " [ERROR]" if tc.is_error else ""
        lines.append(f"--- step {i}: {tc.name}{err} ({tc.duration_ms}ms) ---")
        lines.append(f"$ {tc.arguments}")
        lines.append(tc.output or "")
        lines.append("")
    lines.append(f"=== final answer ===\n{trace.output}")
    path.write_text("\n".join(lines))
