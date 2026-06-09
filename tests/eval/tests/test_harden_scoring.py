"""Unit tests for harden.scoring — the objective and the paired acceptance gates.

The gates are the part most likely to silently let a regression through, so they get
explicit adversarial cases: a candidate that keeps the same AGGREGATE pass-rate while
breaking one cell must be rejected, and a noisy "improvement" must not pass the CI.
"""

from __future__ import annotations

from harden.scoring import (
    RunScore,
    efficiency,
    is_confident,
    no_correctness_regression,
)


def _rs(qid, subject, *, correct, score, tokens=1000):
    return RunScore(
        subject=subject,
        question_id=qid,
        correct=correct,
        correctness=1.0 if correct else 0.0,
        tokens=tokens,
        n_tools=3,
        score=score,
    )


def test_efficiency_is_strictly_decreasing_no_cap():
    # No flat ceiling: fewer tokens ALWAYS scores higher, even well under budget.
    assert efficiency(500, 1000) > efficiency(1000, 1000) > efficiency(2000, 1000)
    assert efficiency(1000, 1000) == 0.25  # a run at `budget` tokens -> 0.5**steepness
    assert efficiency(9000, 1000) < 0.02  # blow-up still tanks (convex tail)
    assert efficiency(0, 1000) == 0.0
    assert 0.0 < efficiency(100, 1000) < 1.0  # bounded, never exactly 1


def test_paired_regression_catches_swap_that_aggregate_misses():
    # baseline: q1 correct, q2 wrong. candidate: q1 wrong, q2 correct.
    # Same aggregate pass-rate (50%), but q1 regressed -> must be rejected.
    base = [_rs("q1", "s", correct=True, score=1.0), _rs("q2", "s", correct=False, score=0.0)]
    cand = [_rs("q1", "s", correct=False, score=0.0), _rs("q2", "s", correct=True, score=1.0)]
    assert no_correctness_regression(base, cand) is False


def test_paired_regression_allows_strict_improvement():
    base = [_rs("q1", "s", correct=True, score=1.0), _rs("q2", "s", correct=False, score=0.0)]
    cand = [_rs("q1", "s", correct=True, score=1.0), _rs("q2", "s", correct=True, score=1.0)]
    assert no_correctness_regression(base, cand) is True


def test_missing_cell_is_a_regression():
    base = [_rs("q1", "s", correct=True, score=1.0), _rs("q2", "s", correct=True, score=1.0)]
    cand = [_rs("q1", "s", correct=True, score=1.0)]  # q2 not measured
    assert no_correctness_regression(base, cand) is False


def test_confidence_requires_consistent_gain_across_cells():
    # candidate clearly better on every one of several cells -> confident
    base = [_rs(f"q{i}", "s", correct=True, score=0.3) for i in range(6)]
    cand = [_rs(f"q{i}", "s", correct=True, score=0.7) for i in range(6)]
    assert is_confident(base, cand) is True


def test_confidence_rejects_noise_and_too_few_cells():
    # one cell up, one down, rest flat -> CI lower bound not > 0
    base = [_rs(f"q{i}", "s", correct=True, score=0.5) for i in range(6)]
    cand = (
        [_rs("q0", "s", correct=True, score=0.9)]
        + [_rs(f"q{i}", "s", correct=True, score=0.5) for i in range(1, 5)]
        + [_rs("q5", "s", correct=True, score=0.1)]
    )
    assert is_confident(base, cand) is False
    # too few cells to bootstrap
    assert is_confident(base[:2], cand[:2]) is False
