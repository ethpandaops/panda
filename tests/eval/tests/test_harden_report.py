"""Unit tests for harden.report — a LEAN summary ranked by metrics + a traces-dir pointer."""

from __future__ import annotations

from harden.report import build_proposal_prompt, summarize_record, worst_records
from harness.runner import Question, RunRecord
from harness.scoring import RunScore
from harness.trace import RunTrace, ToolCall


def _record(qid, *, correct, score, tokens=1000, tools=None, output="ans", crashed=False):
    trace = RunTrace(
        question=f"text-{qid}",
        subject="opencode:m:cli",
        output=output,
        tool_calls=tools or [],
        crashed=crashed,
    )
    rs = RunScore(
        subject="opencode:m:cli",
        question_id=qid,
        correct=correct,
        correctness=1.0 if correct else 0.0,
        tokens=tokens,
        n_tools=len(tools or []),
        score=score,
    )
    return RunRecord(question=Question(id=qid, text=f"text-{qid}"), trace=trace, score=rs)


def test_worst_records_ranks_wrong_then_wasteful():
    recs = [
        _record("good", correct=True, score=0.9),
        _record("wrong", correct=False, score=0.0),
        _record("wasteful", correct=True, score=0.2),
    ]
    worst = worst_records(recs, 2)
    assert [r.question.id for r in worst] == ["wrong", "wasteful"]


def test_summary_is_lean_no_raw_output():
    # the summary shows the step SHAPE + answer, but NOT the raw tool output (that's on disk)
    rec = _record(
        "q1", correct=False, score=0.0,
        tools=[ToolCall("bash", 'panda clickhouse query ds "SELECT 1"', "Code: 60. Table missing", True)],
    )
    text = summarize_record(rec)
    assert "q1" in text and "score=0.00" in text
    assert "bash!" in text  # tool name + error marker (the shape)
    assert "Code: 60. Table missing" not in text  # raw output stays out of the summary
    assert "datasource" not in text


def test_crashed_record_is_legible():
    rec = _record("q1", correct=False, score=0.0, crashed=True)
    rec.trace.error = "Timeout"
    assert "CRASHED: Timeout" in summarize_record(rec)


def test_prompt_has_objective_rules_and_traces_pointer():
    prompt = build_proposal_prompt(
        [_record("q1", correct=False, score=0.0)], traces_dir="/runs/abc/traces"
    )
    assert "encode the ANSWER" in prompt  # anti-leakage
    assert "PLACEMENT" in prompt  # separation of concerns
    assert "relieve pressure the TEST creates" in prompt  # no eval-infra gaming
    assert "q1" in prompt  # the run is summarized
    assert "/runs/abc/traces" in prompt  # the proposer is pointed at the full traces on disk
