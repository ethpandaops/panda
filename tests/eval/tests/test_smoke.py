"""Fast CI smoke tests for ethpandaops-panda.

Simple single-turn questions that verify the whole pipeline works end to end —
the agent can reach each datasource, run a query, and return a plausible answer.
Run with ``pytest -m smoke`` (or ``panda-eval --category smoke``) on every commit.
"""

from __future__ import annotations

import json
from typing import TYPE_CHECKING, Callable

import pytest
from deepeval.metrics import TaskCompletionMetric
from deepeval.test_case import LLMTestCase, ToolCall

from cases.loader import load_test_cases
from config.evaluator import get_evaluator_model
from conftest import CostTracker, TraceRecorder

if TYPE_CHECKING:
    from config.settings import EvalSettings

pytestmark = pytest.mark.smoke

_test_cases = load_test_cases("smoke.yaml")


def _get_test_ids() -> list[str]:
    return [tc.id for tc in _test_cases]


def _get_test_case(test_id: str):
    for tc in _test_cases:
        if tc.id == test_id:
            return tc
    raise ValueError(f"Test case not found: {test_id}")


@pytest.mark.asyncio
@pytest.mark.parametrize("test_id", _get_test_ids())
async def test_smoke(
    test_id: str,
    agent,
    eval_settings: EvalSettings,
    cost_tracker: CostTracker,
    trace_recorder: TraceRecorder,
    record_property: Callable[[str, object], None],
) -> None:
    """Run one smoke question and assert its metrics pass."""
    test_case = _get_test_case(test_id)

    result = await agent.execute(test_case.input, test_id=test_id)

    if result.is_error:
        pytest.fail(f"Agent execution failed: {result.error_message}")

    llm_test_case = LLMTestCase(
        input=test_case.input,
        actual_output=result.output,
        expected_tools=[ToolCall(name=t) for t in test_case.expected_tools],
        tools_called=[ToolCall(name=tc.name) for tc in result.tool_calls],
        additional_metadata={
            "resources_read": result.resources_read,
            "tool_calls": [
                {"name": tc.name, "input": tc.input, "result": tc.result}
                for tc in result.tool_calls
            ],
            "cost_usd": result.total_cost_usd,
            "tokens": {"input": result.input_tokens, "output": result.output_tokens},
            "network": test_case.network,
        },
    )

    evaluator = get_evaluator_model(eval_settings.evaluator_model)
    judge_cost_before = getattr(evaluator, "total_cost_usd", 0.0)
    judge_in_before = getattr(evaluator, "total_input_tokens", 0)
    judge_out_before = getattr(evaluator, "total_output_tokens", 0)

    # The agent must have actually used a tool (any tool — bash for the CLI route,
    # execute_python/search for the MCP route), not just hallucinated an answer.
    if not result.tool_calls:
        pytest.fail(f"{test_id}: agent answered without using any tools")

    # Smoke gate is route-agnostic: just "did the agent answer the question?".
    # The CLI route uses opencode's `bash` tool rather than the panda MCP tools, so
    # MCP-tool-name metrics don't apply here — those live in the full suite.
    # a_measure (vs deepeval's evaluate()) keeps the terminal quiet; the per-question
    # one-liner is emitted by conftest's pytest_runtest_logreport from record_property.
    metric = TaskCompletionMetric(
        threshold=test_case.metrics.get(
            "task_completion", eval_settings.task_completion_threshold
        ),
        model=evaluator,
    )
    await metric.a_measure(llm_test_case, _show_indicator=False)
    metrics_data = [{
        "name": getattr(metric, "__name__", "Task Completion"),
        "score": float(metric.score or 0.0),
        "passed": bool(metric.success),
        "reason": metric.reason,
    }]

    record_property("smoke", json.dumps({
        "id": test_id,
        "passed": all(m["passed"] for m in metrics_data),
        "score": metrics_data[0]["score"],
        "duration_s": round(result.duration_ms / 1000, 1),
        "cost_usd": result.total_cost_usd or 0.0,
        "tools": len(result.tool_calls),
        "answer": (result.output or "")[:80],
    }))

    if eval_settings.track_costs:
        cost_tracker.record(
            test_id=test_id,
            model=eval_settings.model,
            input_tokens=result.input_tokens,
            output_tokens=result.output_tokens,
            cost_usd=result.total_cost_usd,
            duration_ms=result.duration_ms,
            judge_cost_usd=getattr(evaluator, "total_cost_usd", 0.0) - judge_cost_before,
            judge_input_tokens=getattr(evaluator, "total_input_tokens", 0) - judge_in_before,
            judge_output_tokens=getattr(evaluator, "total_output_tokens", 0) - judge_out_before,
        )

    trace_recorder.record(
        test_id=test_id,
        input_prompt=test_case.input,
        output=result.output,
        tool_calls=[
            {"name": tc.name, "input": tc.input, "result": tc.result}
            for tc in result.tool_calls
        ],
        metrics=metrics_data,
        cost_usd=result.total_cost_usd,
        duration_ms=result.duration_ms,
        input_tokens=result.input_tokens,
        output_tokens=result.output_tokens,
        is_error=result.is_error,
        error_message=result.error_message,
        langfuse=agent.langfuse,
        trace_id=agent.current_trace_id,
        session_id=agent.langfuse_session_id,
        category="smoke",
        expected_output=getattr(test_case, "description", None),
        dataset_metadata={
            "network": test_case.network,
            "tags": getattr(test_case, "tags", []),
            "metrics": test_case.metrics,
        },
    )
    agent.flush()

    failed = [m for m in metrics_data if not m["passed"]]
    if failed:
        msg = "\n".join(
            f"  - {m['name']}: score={m['score']:.2f}, reason={m['reason']}" for m in failed
        )
        pytest.fail(f"Metrics failed for {test_id}:\n{msg}")
