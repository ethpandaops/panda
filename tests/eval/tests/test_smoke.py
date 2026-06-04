"""Fast CI smoke tests for ethpandaops-panda.

Simple single-turn questions that verify the whole pipeline works end to end —
the agent can reach each datasource, run a query, and return a plausible answer.
Run with ``pytest -m smoke`` (or ``panda-eval --category smoke``) on every commit.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

import pytest
from deepeval import evaluate
from deepeval.metrics import TaskCompletionMetric, ToolCorrectnessMetric
from deepeval.test_case import LLMTestCase, ToolCall

from cases.loader import load_test_cases
from config.evaluator import get_evaluator_model
from conftest import CostTracker, TraceRecorder
from metrics.data_quality import create_data_plausibility_metric
from metrics.datasource import DataSourceMetric
from metrics.resource_discovery import ResourceDiscoveryMetric

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
) -> None:
    """Run one smoke question and assert its metrics pass."""
    test_case = _get_test_case(test_id)

    result = await agent.execute(test_case.input, test_id=test_id)

    if eval_settings.verbose:
        print(f"\n  Smoke: {test_id}")
        print(f"  Cost: ${result.total_cost_usd or 0:.6f}  "
              f"Tokens: {result.input_tokens} in / {result.output_tokens} out  "
              f"Duration: {result.duration_ms}ms")
        print(f"  Tools: {[tc.name for tc in result.tool_calls]}")
        print(f"  Answer: {(result.output or '')[:200]}")

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

    metrics = [
        ToolCorrectnessMetric(
            threshold=test_case.metrics.get(
                "tool_correctness", eval_settings.tool_correctness_threshold
            ),
            model=evaluator,
        ),
        TaskCompletionMetric(
            threshold=test_case.metrics.get(
                "task_completion", eval_settings.task_completion_threshold
            ),
            model=evaluator,
        ),
    ]
    if "data_plausibility" in test_case.metrics:
        metrics.append(
            create_data_plausibility_metric(network=test_case.network, model=evaluator)
        )
    metrics.append(
        ResourceDiscoveryMetric(threshold=eval_settings.resource_discovery_threshold)
    )
    if test_case.expected_tables:
        metrics.append(
            DataSourceMetric(
                expected_tables=test_case.expected_tables,
                expected_datasource=test_case.expected_datasource,
                expected_columns=test_case.expected_columns,
                require_all_tables=test_case.require_all_tables,
                threshold=1.0,
            )
        )

    eval_results = evaluate(test_cases=[llm_test_case], metrics=metrics)

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
        metrics=[
            {"name": m.name, "score": m.score, "passed": m.success, "reason": m.reason}
            for m in eval_results.test_results[0].metrics_data
        ],
        cost_usd=result.total_cost_usd,
        duration_ms=result.duration_ms,
        input_tokens=result.input_tokens,
        output_tokens=result.output_tokens,
        is_error=result.is_error,
        error_message=result.error_message,
        langfuse=agent.langfuse,
        trace_id=agent.current_trace_id,
    )
    agent.flush()

    failed = [
        (r.name, r.score, r.reason)
        for r in eval_results.test_results[0].metrics_data
        if not r.success
    ]
    if failed:
        msg = "\n".join(f"  - {n}: score={s:.2f}, reason={r}" for n, s, r in failed)
        pytest.fail(f"Metrics failed for {test_id}:\n{msg}")
