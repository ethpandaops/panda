"""Scoring — the objective the optimizer climbs, and the acceptance gates.

Per-run score (deliberately simple):

    0                    if the answer is wrong (or the run crashed)
    efficiency(tokens)   if correct        # scaled by how little context it burned

Why tokens, not tool-count: tokens is the real cost, and it also catches
payload/description bloat (the Goodhart failure mode) in a single number — so a
tool description that balloons to "help" one question pays for itself everywhere.

Why a STEEP (convex) efficiency curve: a single blow-up run then scores ~0 and
tanks the mean, so the plain mean is already tail-sensitive. That's where we get
"punish the 37-call disaster" from — not from a basket of p90/p95/variance terms.

A candidate's score is the MEAN of per-run scores over (variations x K runs x
subjects). Two acceptance checks live OUTSIDE the score, as gates (not extra
objective terms), because folding them into the metric makes it gameable:
  - ``no_correctness_regression`` — no previously-correct work may go wrong.
  - ``is_confident`` — accept on a real gap over enough runs, not a noisy mean.
"""

from __future__ import annotations

import random
import statistics
from dataclasses import dataclass

from harden.trace import RunTrace


def efficiency(tokens: int, budget: int, steepness: float = 2.0) -> float:
    """1.0 at/under ``budget`` tokens, decaying convexly above it.

    ``budget`` is the token cost of a "good" run; ``steepness`` > 1 makes blow-ups
    score near zero so the mean feels the tail. budget/2x -> ~0.25, budget/9x -> ~0.01.
    """
    if tokens <= 0:
        return 0.0
    return min(1.0, budget / tokens) ** steepness


@dataclass
class RunScore:
    """Per-run scoring result (+ raw signals kept for diagnosis, not for the objective).

    ``question_id`` and ``subject`` together identify the CELL a run belongs to. The
    gates are paired by cell — "did THIS question on THIS subject get worse?" — which a
    bare aggregate would hide (one question improving while another silently regresses).
    """

    subject: str
    question_id: str
    correct: bool
    correctness: float  # judge's raw 0..1
    tokens: int
    n_tools: int
    score: float  # 0 if wrong/crashed, else efficiency(tokens)


def score_run(
    trace: RunTrace,
    *,
    correct: bool,
    correctness: float,
    budget: int,
    steepness: float = 2.0,
    question_id: str = "",
) -> RunScore:
    ok = correct and not trace.crashed
    score = efficiency(trace.total_tokens, budget, steepness) if ok else 0.0
    return RunScore(
        subject=trace.subject,
        question_id=question_id,
        correct=ok,
        correctness=correctness,
        tokens=trace.total_tokens,
        n_tools=trace.n_tools,
        score=score,
    )


def candidate_score(runs: list[RunScore]) -> float:
    """Mean per-run score over everything (questions x K x subjects)."""
    return statistics.mean(rs.score for rs in runs) if runs else 0.0


def pass_rate(runs: list[RunScore]) -> float:
    return statistics.mean(1.0 if rs.correct else 0.0 for rs in runs) if runs else 0.0


def _cell(rs: RunScore) -> tuple[str, str]:
    return (rs.question_id, rs.subject)


def _by_cell(runs: list[RunScore]) -> dict[tuple[str, str], list[RunScore]]:
    cells: dict[tuple[str, str], list[RunScore]] = {}
    for rs in runs:
        cells.setdefault(_cell(rs), []).append(rs)
    return cells


def no_correctness_regression(baseline: list[RunScore], candidate: list[RunScore]) -> bool:
    """Gate: no (question, subject) cell may get LESS correct than baseline.

    Paired, not aggregate: a candidate where q1 improves and q2 breaks keeps the same
    overall pass-rate but is a regression — this catches it. Cells absent from the
    candidate count as a regression (we can't show the work didn't get worse).
    """
    base = _by_cell(baseline)
    cand = _by_cell(candidate)
    for cell, base_runs in base.items():
        cand_runs = cand.get(cell)
        if not cand_runs:
            return False
        if pass_rate(cand_runs) < pass_rate(base_runs) - 1e-9:
            return False
    return True


def _paired_cell_deltas(baseline: list[RunScore], candidate: list[RunScore]) -> list[float]:
    """Per-cell (candidate_mean - baseline_mean) over cells present in BOTH."""
    base = _by_cell(baseline)
    cand = _by_cell(candidate)
    deltas: list[float] = []
    for cell, base_runs in base.items():
        cand_runs = cand.get(cell)
        if cand_runs:
            deltas.append(
                statistics.mean(r.score for r in cand_runs)
                - statistics.mean(r.score for r in base_runs)
            )
    return deltas


def is_confident(
    baseline: list[RunScore],
    candidate: list[RunScore],
    *,
    min_cells: int = 3,
    confidence: float = 0.95,
    resamples: int = 2000,
    seed: int = 1234,
) -> bool:
    """Gate: accept only if the improvement is real, not noise.

    Paired bootstrap over (question, subject) cell deltas: resample cells with
    replacement, and require the lower bound of the ``confidence`` interval on the mean
    cell-delta to be > 0. Pairing controls for the huge per-question effort variance;
    the CI controls for "got lucky on K runs". Deterministic (seeded) so a given
    baseline/candidate pair always gates the same way.
    """
    deltas = _paired_cell_deltas(baseline, candidate)
    if len(deltas) < min_cells:
        return False
    rng = random.Random(seed)
    n = len(deltas)
    means: list[float] = []
    for _ in range(resamples):
        sample = [deltas[rng.randrange(n)] for _ in range(n)]
        means.append(statistics.mean(sample))
    means.sort()
    lower = means[int((1.0 - confidence) * resamples)]
    return lower > 0.0
