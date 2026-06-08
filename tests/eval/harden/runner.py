"""Runner — measure one candidate harness state across questions x subjects x K runs.

This is the "measure a candidate" capability the optimizer loop sits on top of. It is
deliberately NOT the optimizer: it takes the world as it is (the panda source / docs /
tool shapes currently on disk) and reports how well agents do, with confidence.

Why K runs: a single agent run's effort is wildly noisy (the same question has come
back in 4, 19, and 37 tool calls, all correct). Scoring one run optimizes that noise;
the candidate score is the MEAN over (questions x subjects x K) so the signal is effort
QUALITY, not luck. See harden.scoring for the objective and the gates.

This module is harness-agnostic: it never imports Langfuse or opencode. Each run's
high-fidelity trace is pushed by the harness itself; the runner hands the harness a
generic scores dict via the optional ``record()`` seam, and the harness (if it has a
client) attaches them to that run's trace and links the dataset run. Gating runs on the
in-process scores, so the loop is correct with telemetry entirely absent.
"""

from __future__ import annotations

import asyncio
import statistics
from dataclasses import dataclass, field

from harden.judge import Judge
from harden.scoring import RunScore, candidate_score, pass_rate, score_run
from harden.subject import Subject
from harden.trace import RunTrace


@dataclass
class Question:
    """A question to measure, with a stable id for paired scoring / dataset linking.

    ``reference`` / ``reference_query`` carry optional ground truth so the judge can grade
    correctness against it rather than mere task completion (see harden.judge)."""

    id: str
    text: str
    reference: str = ""
    reference_query: str = ""
    reference_query_datasource: str = "clickhouse-refined"


@dataclass
class RunRecord:
    """One measured run kept whole: the raw trace plus its score. The proposer reads the
    raw trace; the gates read the score."""

    question: Question
    trace: RunTrace
    score: RunScore


@dataclass
class CandidateResult:
    """The measured quality of one harness state — what gates compare."""

    runs: list[RunScore] = field(default_factory=list)
    records: list[RunRecord] = field(default_factory=list)  # raw traces, for the proposer
    score: float = 0.0  # mean per-run score (the objective)
    pass_rate: float = 0.0  # correctness rate (the no-regression floor)
    by_subject: dict[str, float] = field(default_factory=dict)  # per-subject mean score


async def run_candidate(
    questions: list[Question],
    subjects: list[Subject],
    judge: Judge,
    *,
    k: int = 3,
    budget: int,
    steepness: float = 2.0,
    concurrency: int = 6,
    run_name: str | None = None,
    category: str | None = None,
    branch: str | None = None,
    on_run=None,
) -> CandidateResult:
    """Measure the current harness state over every (question, subject) K times.

    The (question, subject, k) runs are independent, so they fan out concurrently up to
    ``concurrency`` at a time (agent runs dominate wall-clock; K-averaging needs many of
    them). ``budget`` is the token cost of a "good" run (the efficiency knee). ``on_run``
    is an optional callback ``(question, RunScore, RunTrace) -> None`` for live progress.
    """
    runs: list[RunScore] = []
    records: list[RunRecord] = []
    by_subject: dict[str, list[float]] = {}
    sem = asyncio.Semaphore(concurrency)

    async def one(question: Question, subject: Subject) -> None:
        async with sem:
            trace = await subject.run(question.text)
        verdict = await judge.judge(
            trace,
            reference=question.reference,
            reference_query=question.reference_query,
            reference_query_datasource=question.reference_query_datasource,
        )
        rs = score_run(
            trace,
            correct=verdict.correct,
            correctness=verdict.correctness,
            budget=budget,
            steepness=steepness,
            question_id=question.id,
        )
        # asyncio is single-threaded, so these appends never interleave mid-statement.
        runs.append(rs)
        records.append(RunRecord(question=question, trace=trace, score=rs))
        by_subject.setdefault(subject.name, []).append(rs.score)

        record = getattr(subject, "record", None)
        if record is not None:
            record(
                trace,
                {
                    "harden_score": rs.score,
                    "correct": 1.0 if rs.correct else 0.0,
                    "correctness": rs.correctness,
                    "tokens": float(rs.tokens),
                    "tool_count": float(rs.n_tools),
                },
                comment=verdict.reason,
                category=category,
                branch=branch,
                run_name=run_name,
                question_id=question.id,
                question_text=question.text,
            )
        if on_run is not None:
            on_run(question, rs, trace)

    await asyncio.gather(*(one(q, s) for q in questions for s in subjects for _ in range(k)))

    for subject in subjects:
        flush = getattr(subject, "flush", None)
        if flush is not None:
            try:
                flush()
            except Exception:  # noqa: BLE001 - flush is best-effort
                pass

    return CandidateResult(
        runs=runs,
        records=records,
        score=candidate_score(runs),
        pass_rate=pass_rate(runs),
        by_subject={name: statistics.mean(s) for name, s in by_subject.items() if s},
    )
