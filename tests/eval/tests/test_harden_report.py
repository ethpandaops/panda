"""Unit tests for harden.report — the proposer prompt is a RAW dump, ranked by metrics."""

from __future__ import annotations

from harden.report import build_proposal_prompt, format_record, worst_records
from harden.runner import Question, RunRecord
from harden.scoring import RunScore
from harden.trace import RunTrace, ToolCall


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


def test_format_record_dumps_raw_steps_not_digests():
    rec = _record(
        "q1",
        correct=False,
        score=0.0,
        tools=[
            ToolCall(
                "bash", 'panda clickhouse query ds "SELECT 1"', "Code: 60. Table missing", True
            ),
        ],
    )
    text = format_record(rec)
    # raw command + raw output present verbatim; no derived "kind"/"datasource"/"error_code"
    assert 'panda clickhouse query ds "SELECT 1"' in text
    assert "Code: 60. Table missing" in text
    assert "[ERROR]" in text
    assert "datasource" not in text and "error_code" not in text


def test_format_record_truncates_huge_output():
    big = "x" * 5000
    rec = _record("q1", correct=True, score=0.5, tools=[ToolCall("bash", "cmd", big)])
    text = format_record(rec, max_output_chars=100)
    assert "[+4900 chars]" in text


def test_crashed_record_is_legible():
    rec = _record("q1", correct=False, score=0.0, crashed=True)
    rec.trace.error = "Timeout"
    assert "CRASHED: Timeout" in format_record(rec)


def test_prompt_has_objective_and_no_hardcoding_clause():
    prompt = build_proposal_prompt([_record("q1", correct=False, score=0.0)])
    assert "Do NOT hardcode" in prompt
    assert "generaliz" in prompt.lower()
    assert "text-q1" in prompt  # the question is included
