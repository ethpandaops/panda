"""Scoring — the objective the optimizer climbs, and the acceptance gates.

Per-run score (deliberately simple):

    0                    if the answer is wrong (or the run crashed)
    efficiency(tokens)   if correct        # scaled by how little context it burned

Why tokens, not tool-count: tokens is the real cost, and it also catches
payload/description bloat (the Goodhart failure mode) in a single number — so a
tool description that balloons to "help" one question pays for itself everywhere.

Why a strictly-decreasing efficiency curve (no cap): fewer tokens must ALWAYS score
higher, or the optimizer has no gradient where it matters. The old "1.0 at/under
budget" cap made every sub-budget run a tie, so it could never reward leanness. The
curve stays convex (steepness > 1) so a single blow-up run still scores ~0 and tanks
the mean — "punish the 37-call disaster" — without a basket of p90/p95/variance terms.

A candidate's score is the MEAN of per-run scores over (variations x K runs x
subjects). Two acceptance checks live OUTSIDE the score, as gates (not extra
objective terms), because folding them into the metric makes it gameable:
  - ``no_correctness_regression`` — no cell may get SIGNIFICANTLY less correct.
  - ``is_confident`` — accept on a real gap over enough runs, not a noisy mean.

Both gates work on (question, subject) CELLS, pooling a question's K repeats and
paraphrase variations into one unit. That's deliberate (per Anthropic's eval-statistics
guidance): repeats of one question are correlated and paraphrases share difficulty, so
they are one cluster, not independent samples — treating each run as independent makes
every test wildly overconfident.
"""

from __future__ import annotations

import math
import random
import statistics
from collections.abc import Callable
from dataclasses import dataclass

from harness.trace import RunTrace


def efficiency(tokens: int, ref: float, steepness: float = 2.0) -> float:
    """Token efficiency in (0, 1], STRICTLY DECREASING — fewer tokens always score higher.

    No cap and no magic constant: ``ref`` is the question's OWN baseline cost (the median
    tokens of its correct baseline runs), derived per run, not a number anyone picks. A run
    at the baseline's cost scores ``0.5**steepness`` (0.25 at steepness 2); a leaner candidate
    rises toward 1.0, a heavier one decays smoothly (2x ref -> 0.11, 4x -> 0.04 at steepness
    2). ``steepness`` > 1 keeps the tail convex so a blow-up still tanks the mean.
    """
    if tokens <= 0 or ref <= 0:
        return 0.0
    return (ref / (ref + tokens)) ** steepness


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
    reason: str = ""  # the grader's stated reason (diagnosis only, not scored)


def score_run(
    trace: RunTrace,
    *,
    correct: bool,
    correctness: float,
    ref: float,
    steepness: float = 2.0,
    question_id: str = "",
    reason: str = "",
) -> RunScore:
    ok = correct and not trace.crashed
    score = efficiency(trace.total_tokens, ref, steepness) if ok else 0.0
    return RunScore(
        subject=trace.subject,
        question_id=question_id,
        correct=ok,
        correctness=correctness,
        tokens=trace.total_tokens,
        n_tools=trace.n_tools,
        score=score,
        reason=reason,
    )


def candidate_score(runs: list[RunScore]) -> float:
    """Mean per-run score over everything (questions x K x subjects)."""
    return statistics.mean(rs.score for rs in runs) if runs else 0.0


def pass_rate(runs: list[RunScore]) -> float:
    return statistics.mean(1.0 if rs.correct else 0.0 for rs in runs) if runs else 0.0


def filter_runs(runs: list[RunScore], question_ids: set[str]) -> list[RunScore]:
    """Runs whose question is in ``question_ids`` — used to gate on held-out questions
    the proposer never saw, so a change that only memorizes the train questions can't pass."""
    return [rs for rs in runs if rs.question_id in question_ids]


def _cell(rs: RunScore) -> tuple[str, str]:
    return (rs.question_id, rs.subject)


def _by_cell(runs: list[RunScore]) -> dict[tuple[str, str], list[RunScore]]:
    cells: dict[tuple[str, str], list[RunScore]] = {}
    for rs in runs:
        cells.setdefault(_cell(rs), []).append(rs)
    return cells


def _fisher_drop_p(base_runs: list[RunScore], cand_runs: list[RunScore]) -> float:
    """One-sided Fisher exact p-value that the candidate's pass count in this cell is as
    low as observed under H0 "same pass probability as baseline" (hypergeometric tail)."""
    b_n, c_n = len(base_runs), len(cand_runs)
    b_pass = sum(1 for r in base_runs if r.correct)
    c_pass = sum(1 for r in cand_runs if r.correct)
    total, passes = b_n + c_n, b_pass + c_pass
    denom = math.comb(total, c_n)
    lo = max(0, c_n - (total - passes))
    return sum(
        math.comb(passes, x) * math.comb(total - passes, c_n - x)
        for x in range(lo, min(c_pass, passes, c_n) + 1)
    ) / denom


def no_correctness_regression(
    baseline: list[RunScore], candidate: list[RunScore], *, alpha: float = 0.05
) -> bool:
    """Gate: no (question, subject) cell may get SIGNIFICANTLY less correct than baseline.

    Paired, not aggregate: a candidate where q1 improves and q2 breaks keeps the same
    overall pass-rate but is a regression — this catches it. Cells absent from the
    candidate count as a regression (we can't show the work didn't get worse).

    "Significantly" is load-bearing: a strict any-drop comparison false-rejects almost
    every candidate under normal judge/agent noise (simulated ~90-100% with our real cell
    geometries), so a drop only counts when a one-sided Fisher exact test says it's
    unlikely to be noise (p <= ``alpha``). At k=3 a total collapse (3/3 -> 0/3, p=0.05)
    still trips; one flaky run does not. k=3 is therefore the POWER FLOOR for cells with
    a single phrasing — below it even a full collapse can't reach significance (pool
    paraphrase variations into the cell, or raise k, to detect smaller drops).
    ``alpha`` is per-cell, NOT Bonferroni-split:
    splitting would leave small cells powerless to flag even a full collapse — we accept
    a slightly higher familywise false-reject rate to keep that power.
    """
    base = _by_cell(baseline)
    cand = _by_cell(candidate)
    for cell, base_runs in base.items():
        cand_runs = cand.get(cell)
        if not cand_runs:
            return False
        if pass_rate(cand_runs) >= pass_rate(base_runs) - 1e-9:
            continue
        if _fisher_drop_p(base_runs, cand_runs) <= alpha:
            return False
    return True


def _paired_cell_stats(
    baseline: list[RunScore], candidate: list[RunScore]
) -> list[tuple[float, float]]:
    """Per-cell (delta, SE(delta)) over cells present in BOTH. SE is computed from the
    within-cell run spread (sqrt(s_b^2/k_b + s_c^2/k_c)); 0.0 when either side has a
    single run (no spread information)."""
    base = _by_cell(baseline)
    cand = _by_cell(candidate)
    stats: list[tuple[float, float]] = []
    for cell, base_runs in base.items():
        cand_runs = cand.get(cell)
        if not cand_runs:
            continue
        b = [r.score for r in base_runs]
        c = [r.score for r in cand_runs]
        delta = statistics.mean(c) - statistics.mean(b)
        se = 0.0
        if len(b) >= 2 and len(c) >= 2:
            se = math.sqrt(
                statistics.variance(b) / len(b) + statistics.variance(c) / len(c)
            )
        stats.append((delta, se))
    return stats


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
    log: Callable[[str], None] | None = None,
) -> bool:
    """Gate: accept only if the improvement is real, not noise.

    Paired sign-flip PERMUTATION test over (question, subject) cell deltas: under
    H0 "no real difference" each cell's delta is equally likely to have either sign, so
    we enumerate sign-flips and ask how often a flipped mean is >= the observed mean.
    Accept when that p-value is <= 1 - ``confidence``. Pairing controls for the huge
    per-question effort variance; the permutation test is exact (not anti-conservative
    like a bootstrap at small N) and fully deterministic — enumeration, no RNG.

    Tied cells (zero delta — e.g. a cell already saturated at 1.0 in both states)
    carry no directional information and are DROPPED, per standard sign-test
    convention. Treating them as failures would make the gate permanently unwinnable
    once any baseline cell is perfect. They are exactly p-neutral in the permutation
    branch anyway; dropping them only affects branch selection and unanimity.

    Small-N fallback: with fewer informative cells than a permutation test can ever
    reach ``confidence`` on (2^-n > alpha, e.g. under 5 cells at 95%), require
    UNANIMOUS improvement among informative cells. In this branch a cell is
    informative only if |delta| exceeds the cell's own standard error (measured from
    its run spread, needs k>=2 on both sides): a judge-noise-sized wobble neither
    vetoes three solid wins nor counts as a win itself. The permutation branch keeps
    every non-tied cell — pricing noisy deltas is what that test is for.
    """
    stats = _paired_cell_stats(baseline, candidate)
    if len(stats) < min_cells:
        return False
    nontied = [(d, se) for d, se in stats if abs(d) > 1e-9]
    ties = len(stats) - len(nontied)
    if ties and log:
        log(f"confidence gate: dropped {ties} tied cell(s); {len(nontied)} informative")
    n = len(nontied)
    if n == 0:
        return False  # every cell tied: no evidence either way
    deltas = [d for d, _ in nontied]
    observed = statistics.mean(deltas)
    if observed <= 0:
        return False
    alpha = 1.0 - confidence
    if 0.5**n > alpha:
        beyond_noise = [d for d, se in nontied if abs(d) > se]
        within = n - len(beyond_noise)
        if within and log:
            log(
                f"confidence gate: {within} cell(s) within run-spread noise "
                f"(|delta| <= SE) excluded from unanimity"
            )
        if not beyond_noise:
            return False  # nothing distinguishable from noise
        return all(d > 0 for d in beyond_noise)
    if n <= 18:  # exact enumeration of all 2^n sign patterns
        hits, total = 0, 2**n
        for mask in range(total):
            flipped = sum(d if (mask >> i) & 1 else -d for i, d in enumerate(deltas))
            if flipped / n >= observed - 1e-12:
                hits += 1
        return hits / total <= alpha
    # Too many cells to enumerate: seeded Monte Carlo over sign patterns (deterministic).
    rng = random.Random(1234)
    resamples = 20000
    hits = sum(
        1
        for _ in range(resamples)
        if sum(d if rng.random() < 0.5 else -d for d in deltas) / n >= observed - 1e-12
    )
    return (hits + 1) / (resamples + 1) <= alpha
