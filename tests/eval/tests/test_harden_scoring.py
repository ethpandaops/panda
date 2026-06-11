"""Unit tests for harness.scoring — the objective and the paired acceptance gates.

The gates are the part most likely to silently let a regression through, so they get
explicit adversarial cases: a candidate that keeps the same AGGREGATE pass-rate while
breaking one cell must be rejected, and a noisy "improvement" must not pass the CI.
"""

from __future__ import annotations

from harness.scoring import (
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
    # No flat ceiling: fewer tokens ALWAYS scores higher, even well below the reference.
    assert efficiency(500, 1000) > efficiency(1000, 1000) > efficiency(2000, 1000)
    assert efficiency(1000, 1000) == 0.25  # a run at the reference cost -> 0.5**steepness
    assert efficiency(9000, 1000) < 0.02  # blow-up still tanks (convex tail)
    assert efficiency(0, 1000) == 0.0
    assert 0.0 < efficiency(100, 1000) < 1.0  # bounded, never exactly 1


def _cell(qid, *, passed, total, score_ok=1.0):
    """One cell's runs: `passed` correct out of `total` (k repeats / paraphrases pooled)."""
    return [
        _rs(qid, "s", correct=(i < passed), score=score_ok if i < passed else 0.0)
        for i in range(total)
    ]


def test_paired_regression_catches_collapse_that_aggregate_misses():
    # baseline: q1 solid (3/3), q2 broken (0/3). candidate: q1 collapses, q2 fixed.
    # Same aggregate pass-rate (50%), but q1's total collapse is a significant drop
    # (Fisher one-sided p=0.05 at 3/3 -> 0/3) -> must be rejected.
    base = _cell("q1", passed=3, total=3) + _cell("q2", passed=0, total=3)
    cand = _cell("q1", passed=0, total=3) + _cell("q2", passed=3, total=3)
    assert no_correctness_regression(base, cand) is False


def test_one_flaky_run_is_not_a_regression():
    # A 3/3 -> 2/3 dip in one cell is indistinguishable from judge/agent noise
    # (Fisher p=0.5): the strict any-drop gate false-rejected ~always on this.
    base = _cell("q1", passed=3, total=3) + _cell("q2", passed=3, total=3)
    cand = _cell("q1", passed=2, total=3) + _cell("q2", passed=3, total=3)
    assert no_correctness_regression(base, cand) is True


def test_significant_drop_in_a_large_cell_is_a_regression():
    # With 18 runs/cell (6 phrasings x k=3), a 18/18 -> 12/18 drop is significant.
    base = _cell("q1", passed=18, total=18)
    cand = _cell("q1", passed=12, total=18)
    assert no_correctness_regression(base, cand) is False


def test_paired_regression_allows_strict_improvement():
    base = _cell("q1", passed=3, total=3) + _cell("q2", passed=0, total=3)
    cand = _cell("q1", passed=3, total=3) + _cell("q2", passed=3, total=3)
    assert no_correctness_regression(base, cand) is True


def test_missing_cell_is_a_regression():
    base = _cell("q1", passed=3, total=3) + _cell("q2", passed=3, total=3)
    cand = _cell("q1", passed=3, total=3)  # q2 not measured
    assert no_correctness_regression(base, cand) is False


def test_confidence_requires_consistent_gain_across_cells():
    # candidate clearly better on every one of 6 cells -> permutation p = 1/64 -> confident
    base = [_rs(f"q{i}", "s", correct=True, score=0.3) for i in range(6)]
    cand = [_rs(f"q{i}", "s", correct=True, score=0.7) for i in range(6)]
    assert is_confident(base, cand) is True


def test_confidence_rejects_noise_and_too_few_cells():
    # one cell up, one down, rest flat -> mean delta 0 -> not confident
    base = [_rs(f"q{i}", "s", correct=True, score=0.5) for i in range(6)]
    cand = (
        [_rs("q0", "s", correct=True, score=0.9)]
        + [_rs(f"q{i}", "s", correct=True, score=0.5) for i in range(1, 5)]
        + [_rs("q5", "s", correct=True, score=0.1)]
    )
    assert is_confident(base, cand) is False
    # below min_cells -> never confident
    assert is_confident(base[:2], cand[:2]) is False


def test_confidence_small_n_fallback_requires_unanimity():
    # 4 cells can't reach 95% by permutation (2^-4 > 0.05): unanimous improvement
    # passes, any single down-cell fails.
    base = [_rs(f"q{i}", "s", correct=True, score=0.4) for i in range(4)]
    up = [_rs(f"q{i}", "s", correct=True, score=0.8) for i in range(4)]
    assert is_confident(base, up) is True
    mixed = up[:3] + [_rs("q3", "s", correct=True, score=0.3)]
    assert is_confident(base, mixed) is False


def test_confidence_saturated_cell_does_not_make_gate_unwinnable():
    # q0 is perfect in both states (delta 0): a tie carries no evidence and must be
    # dropped, not counted as a failure — otherwise nothing could ever commit.
    base = [_rs("q0", "s", correct=True, score=1.0)] + [
        _rs(f"q{i}", "s", correct=True, score=0.4) for i in range(1, 4)
    ]
    cand = [_rs("q0", "s", correct=True, score=1.0)] + [
        _rs(f"q{i}", "s", correct=True, score=0.8) for i in range(1, 4)
    ]
    assert is_confident(base, cand, min_cells=3) is True


def test_confidence_all_ties_is_no_evidence():
    base = [_rs(f"q{i}", "s", correct=True, score=1.0) for i in range(4)]
    cand = [_rs(f"q{i}", "s", correct=True, score=1.0) for i in range(4)]
    assert is_confident(base, cand, min_cells=3) is False


def test_confidence_tie_plus_regression_still_fails():
    base = [_rs("q0", "s", correct=True, score=1.0)] + [
        _rs(f"q{i}", "s", correct=True, score=0.5) for i in range(1, 4)
    ]
    cand = (
        [_rs("q0", "s", correct=True, score=1.0)]
        + [_rs(f"q{i}", "s", correct=True, score=0.8) for i in range(1, 3)]
        + [_rs("q3", "s", correct=True, score=0.4)]
    )
    assert is_confident(base, cand, min_cells=3) is False


def _cell_runs(qid: str, scores: list[float]) -> list:
    return [_rs(qid, "s", correct=True, score=v) for v in scores]


def test_confidence_noise_sized_dip_does_not_veto_unanimity():
    # Three solid wins + one dip well inside that cell's own run spread: the dip is
    # not distinguishable from judge noise and must not veto the commit.
    base = (
        _cell_runs("q0", [0.4, 0.4])
        + _cell_runs("q1", [0.4, 0.4])
        + _cell_runs("q2", [0.4, 0.4])
        + _cell_runs("q3", [0.4, 0.6])  # mean 0.5, sd 0.141
    )
    cand = (
        _cell_runs("q0", [0.8, 0.8])
        + _cell_runs("q1", [0.8, 0.8])
        + _cell_runs("q2", [0.8, 0.8])
        + _cell_runs("q3", [0.34, 0.6])  # mean 0.47: delta -0.03 << SE
    )
    assert is_confident(base, cand, min_cells=3) is True


def test_confidence_real_regression_still_vetoes():
    # The dip is far beyond the cell's run spread: a real regression still blocks.
    base = (
        _cell_runs("q0", [0.4, 0.4])
        + _cell_runs("q1", [0.4, 0.4])
        + _cell_runs("q2", [0.4, 0.4])
        + _cell_runs("q3", [0.5, 0.5])
    )
    cand = (
        _cell_runs("q0", [0.8, 0.8])
        + _cell_runs("q1", [0.8, 0.8])
        + _cell_runs("q2", [0.8, 0.8])
        + _cell_runs("q3", [0.1, 0.1])  # delta -0.4, zero spread
    )
    assert is_confident(base, cand, min_cells=3) is False


def test_confidence_k1_keeps_strict_behavior():
    # With a single run per cell there is no spread information (SE=0): any nonzero
    # dip is treated as informative, exactly as before.
    base = [_rs(f"q{i}", "s", correct=True, score=0.4) for i in range(4)]
    cand = [_rs(f"q{i}", "s", correct=True, score=0.8) for i in range(3)] + [
        _rs("q3", "s", correct=True, score=0.39)
    ]
    assert is_confident(base, cand, min_cells=3) is False


def test_confidence_all_within_noise_is_no_evidence():
    # Every delta is inside its cell's spread: nothing distinguishable from noise.
    base = _cell_runs("q0", [0.4, 0.6]) + _cell_runs("q1", [0.4, 0.6]) + _cell_runs(
        "q2", [0.4, 0.6]
    )
    cand = _cell_runs("q0", [0.45, 0.6]) + _cell_runs("q1", [0.4, 0.65]) + _cell_runs(
        "q2", [0.42, 0.62]
    )
    assert is_confident(base, cand, min_cells=3) is False
