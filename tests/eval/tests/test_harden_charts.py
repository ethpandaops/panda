"""Unit tests for harden.charts — the history ledger and the hill-climb renderer.

The renderer must be unkillable from the loop's perspective (a chart bug must not abort
a multi-hour run) and the champion line must only step up on rounds that were not
explicitly rejected.
"""

from __future__ import annotations

import json

from harden.charts import CHART_NAME, HISTORY_NAME, load_history, record, render
from harden.runner import CandidateResult
from harden.scoring import RunScore


def _result(score: float, pass_rate: float, tokens: int = 1000) -> CandidateResult:
    runs = [
        RunScore(
            subject="s", question_id="q1", correct=True, correctness=1.0,
            tokens=tokens, n_tools=2, score=score,
        )
    ]
    return CandidateResult(runs=runs, score=score, pass_rate=pass_rate)


def test_record_appends_history_and_renders(tmp_path):
    record(tmp_path, round_n=0, label="baseline", accepted=None, reason="baseline",
           result=_result(0.2, 0.8, tokens=5000))
    record(tmp_path, round_n=1, label="round1", accepted=True, reason="champion",
           result=_result(0.4, 1.0, tokens=2500), summary="  multi   line\nsummary  ")
    record(tmp_path, round_n=2, label="round2", accepted=False, reason="audit-blocked")

    history = load_history(tmp_path)
    assert [h["round"] for h in history] == [0, 1, 2]
    assert history[0]["measured"] and history[0]["accepted"] is None
    assert history[1]["score"] == 0.4 and history[1]["mean_tokens_correct"] == 2500
    assert history[1]["summary"] == "multi line summary"
    assert history[2]["measured"] is False and "score" not in history[2]
    assert (tmp_path / CHART_NAME).exists()


def test_record_never_raises_on_render_failure(tmp_path):
    # An unwritable run dir breaks both the append and the render: record must
    # swallow it (and surface it via log) instead of killing the loop.
    messages: list[str] = []
    record(tmp_path / "missing" / "nested", round_n=0, label="baseline", accepted=None,
           reason="baseline", result=_result(0.2, 0.8), log=messages.append)
    assert messages and "chart update failed" in messages[0]


def test_render_returns_none_with_no_measured_rounds(tmp_path):
    (tmp_path / HISTORY_NAME).write_text(
        json.dumps({"round": 1, "label": "round1", "measured": False, "reason": "x"}) + "\n"
    )
    assert render(tmp_path) is None


def test_champion_line_ignores_rejected_scores(tmp_path):
    # A rejected round with a HIGHER raw score must not lift the champion line —
    # the hill climb tracks what survived the gates, not what merely scored well.
    record(tmp_path, round_n=0, label="baseline", accepted=None, reason="baseline",
           result=_result(0.2, 0.8))
    record(tmp_path, round_n=1, label="round1", accepted=False, reason="regression",
           result=_result(0.9, 0.5))
    out = render(tmp_path)
    assert out is not None and out.exists()
