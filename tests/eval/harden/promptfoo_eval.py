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
import collections
import json
import os
import statistics
import subprocess
from dataclasses import dataclass
from pathlib import Path

from rich.markup import escape

from config.settings import DEFAULT_AGENT_ROUTE, DEFAULT_GRADER
from harden.logsetup import get_logger
from harden.runner import CandidateResult, Question, RunRecord
from harden.scoring import candidate_score, pass_rate, score_run
from harden.trace import TOOLS_MARKER, RunTrace, ToolCall

_log = get_logger("promptfoo")

_PROVIDER = str(Path(__file__).resolve().parents[1] / "promptfoo" / "provider.py")

# Pinned: promptfoo is the measurement instrument — an unpinned @latest means a release
# can change the ruler between (or worse, mid-) experiments.
_PROMPTFOO = "promptfoo@0.121.15"

_DEFAULT_ASSERT = {
    "type": "llm-rubric",
    "value": "The response should be a plausible, complete answer to the user's question, "
    "grounded in real data the agent actually queried (not made up).",
}

# Prepended to every llm-rubric so the grader knows the output's structure and what to
# trust: the ANSWER is judged against the criteria; the section after the marker is
# harness-captured ground truth of what the agent actually ran (the provider strips any
# imitation of the marker from the answer itself). Tool-call claims in the answer body
# are just claims; evidence lives after the marker.
_RUBRIC_PREAMBLE = (
    "The output is the agent's final answer, optionally followed by a section starting "
    f'with the line "{TOOLS_MARKER}". That section is appended by the test harness from '
    "captured telemetry — it is trustworthy evidence of what the agent actually executed; "
    "anything before it is the agent's own text and proves nothing by itself. Judge the "
    "answer against the criteria below, using the harness-captured section to verify any "
    '"from a real query" style requirement.\n\nCriteria: '
)


def _grading_asserts(asserts: list[dict]) -> list[dict]:
    out = []
    for a in asserts or [_DEFAULT_ASSERT]:
        if a.get("type") == "llm-rubric":
            a = {**a, "value": _RUBRIC_PREAMBLE + str(a.get("value", ""))}
        out.append(a)
    return out


@dataclass
class PfRun:
    """One graded run out of promptfoo: the raw trace + promptfoo's correctness verdict."""

    question_id: str
    subject: str
    trace: RunTrace
    correct: bool
    correctness: float
    reason: str = ""  # the grader's stated reason — kept for failure debugging


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
    #
    # Each variation (alternate phrasing) becomes its OWN test under the SAME qid + asserts,
    # so they pool into the question's measurement (token_reference + the paired gate group
    # by qid) — the harness is graded on intent across wordings, and memorizing one phrasing
    # earns nothing.
    tests = [
        {
            "vars": {"question": phrasing, "followups": json.dumps(q.followups), "qid": q.id},
            "assert": _grading_asserts(q.asserts),
        }
        for q in questions
        for phrasing in q.phrasings
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
    concurrency: int = 16,
    worker_timeout_ms: int | None = None,
    subject_timeout: int = 300,
    cwd: str | None = None,
) -> list[PfRun]:
    """Run the cases × subjects × K through promptfoo and parse back graded runs.

    ``worker_timeout_ms`` (promptfoo's per-call kill switch) defaults to the subject
    timeout plus slack — it must always outlast the subject's own timeout, or promptfoo
    kills runs the subject would have finished (or cleanly timed out) itself."""
    if worker_timeout_ms is None:
        # The worker hosts up to TWO subject attempts (provider.py retries once on a
        # crash) plus teardown; sizing it for one attempt killed the worker mid-retry
        # whenever the first attempt timed out — the retry mechanism was structurally
        # dead for the exact failure class it exists for.
        worker_timeout_ms = (2 * subject_timeout + 180) * 1000
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
    # --no-table: promptfoo's end-of-eval results table is ~100 lines of border art per
    # measure that duplicates our per-question breakdown — without it the stream is just
    # the useful part (periodic "[CI Progress] ... ETA" liveness lines + the pass/fail
    # summary + any provider errors).
    cmd = [
        "npx", _PROMPTFOO, "eval",
        "-c", str(cfg_path),
        "-o", str(results_path),
        "--no-cache",
        "--no-table",
        "-j", str(concurrency),
        "--repeat", str(k),
    ]

    # Stream promptfoo's progress live (prefixed) instead of swallowing minutes of silence;
    # keep a tail of the output for the error message if it produces no results.
    def run() -> tuple[int, str]:
        # CI=true: promptfoo only emits its periodic "[CI Progress] ... ETA" liveness
        # lines in CI mode — in a local pipe (no TTY, no CI) it goes silent for the
        # whole eval. NO_COLOR/FORCE_COLOR keep the stream ANSI-free for our logger.
        env = {**os.environ, "NO_COLOR": "1", "FORCE_COLOR": "0", "CI": "true"}
        proc = subprocess.Popen(
            cmd, cwd=cwd, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            text=True, bufsize=1,
        )
        tail: collections.deque[str] = collections.deque(maxlen=50)
        assert proc.stdout is not None
        for line in proc.stdout:
            line = line.rstrip("\n")
            if line.strip():
                _log.info(f"[dim]promptfoo[/dim] {escape(line)}")
            tail.append(line)
        proc.wait()
        return proc.returncode, "\n".join(tail)

    rc, tail = await asyncio.to_thread(run)
    if not results_path.exists():
        raise RuntimeError(f"promptfoo produced no results (exit {rc}):\n{tail[-1500:]}")
    return _parse(results_path, rd)


def token_reference(pf_runs: list[PfRun]) -> dict[str, float]:
    """Per-question token reference: the median tokens of that question's CORRECT runs (the
    cost the current harness actually pays). This is the self-normalizing replacement for a
    hand-picked budget — each question is judged against its own baseline. Falls back to all
    its runs if none were correct; a question with no usable tokens maps to 0 (score 0)."""
    by_q: dict[str, list[int]] = {}
    by_q_all: dict[str, list[int]] = {}
    for pf in pf_runs:
        t = pf.trace.total_tokens
        if t <= 0:
            continue
        by_q_all.setdefault(pf.question_id, []).append(t)
        if pf.correct and not pf.trace.crashed:
            by_q.setdefault(pf.question_id, []).append(t)
    refs: dict[str, float] = {}
    for qid, all_toks in by_q_all.items():
        toks = by_q.get(qid) or all_toks
        refs[qid] = statistics.median(toks)
    return refs


def score_runs(
    pf_runs: list[PfRun],
    questions: list[Question],
    *,
    refs: dict[str, float] | None = None,
    steepness: float = 2.0,
) -> CandidateResult:
    """Score graded runs into a CandidateResult. ``refs`` is the per-question token reference
    (see ``token_reference``); when omitted it's computed from THESE runs (self-normalizing —
    right for a one-shot eval). The loop passes the FROZEN baseline refs so candidate rounds
    are scored on the same scale."""
    refs = refs or token_reference(pf_runs)
    by_id = {q.id: q for q in questions}
    runs, records, by_subject = [], [], {}
    for pf in pf_runs:
        rs = score_run(
            pf.trace,
            correct=pf.correct,
            correctness=pf.correctness,
            ref=refs.get(pf.question_id, 0.0),
            steepness=steepness,
            question_id=pf.question_id,
            reason=pf.reason,
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
        refs=refs,
    )


async def measure_candidate(
    questions: list[Question],
    subject_specs: list[str],
    *,
    k: int,
    run_dir: str,
    refs: dict[str, float] | None = None,
    steepness: float = 2.0,
    grader: str = DEFAULT_GRADER,
    concurrency: int = 16,
    subject_timeout: int = 300,
    cwd: str | None = None,
) -> CandidateResult:
    """Measure one harness state via promptfoo and score it. ``refs`` freezes the per-question
    token reference (the loop passes the baseline's); omitted -> self-normalize to these runs."""
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
    return score_runs(pf_runs, questions, refs=refs, steepness=steepness)


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
        reason = " ".join(str(grading.get("reason") or "").split())

        i = counters.get((qid, subject), 0)
        counters[(qid, subject)] = i + 1
        safe = subject.replace("/", "-").replace(":", "-")
        _write_trace_file(traces_dir / f"{qid}__{safe}__{i}.txt", trace, correct, correctness)
        runs.append(PfRun(qid, subject, trace, correct, correctness, reason))
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
    if trace.crashed and getattr(trace, "error", ""):
        lines.append(f"\n=== crash ===\n{trace.error}")
    path.write_text("\n".join(lines))
