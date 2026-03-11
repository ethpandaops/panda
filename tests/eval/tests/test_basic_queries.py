"""Tests for basic single-turn queries against ethpandaops-panda."""

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
    from agent.wrapper import MCPAgent
    from config.settings import EvalSettings


# Load test cases at module level for parametrization
_test_cases = load_test_cases("basic_queries.yaml")


def _get_test_ids() -> list[str]:
    """Get test case IDs for pytest parametrization."""
    return [tc.id for tc in _test_cases]


def _get_test_case(test_id: str):
    """Get a test case by ID."""
    for tc in _test_cases:
        if tc.id == test_id:
            return tc
    raise ValueError(f"Test case not found: {test_id}")


@pytest.mark.asyncio
@pytest.mark.parametrize("test_id", _get_test_ids())
async def test_basic_query(
    test_id: str,
    agent: MCPAgent,
    eval_settings: EvalSettings,
    cost_tracker: CostTracker,
    trace_recorder: TraceRecorder,
) -> None:
    """Test a basic single-turn query.

    Args:
        test_id: ID of the test case to run.
        agent: The MCPAgent instance.
        eval_settings: Evaluation settings.
        cost_tracker: Cost tracker for aggregating costs.
        trace_recorder: Trace recorder for saving detailed traces.
    """
    test_case = _get_test_case(test_id)

    # Execute agent (pass test_id for Langfuse trace naming)
    result = await agent.execute(test_case.input, test_id=test_id)

    # Log costs if tracking enabled
    if eval_settings.track_costs:
        cost_tracker.record(
            test_id=test_id,
            model=eval_settings.model,
            input_tokens=result.input_tokens,
            output_tokens=result.output_tokens,
            cost_usd=result.total_cost_usd,
            duration_ms=result.duration_ms,
        )

        if eval_settings.verbose:
            print(f"\n  Test: {test_id}")
            print(f"  Cost: ${result.total_cost_usd or 0:.6f}")
            print(f"  Tokens: {result.input_tokens} in / {result.output_tokens} out")
            print(f"  Duration: {result.duration_ms}ms")
            print(f"  Tools: {[tc.name for tc in result.tool_calls]}")

    # Check for execution errors
    if result.is_error:
        pytest.fail(f"Agent execution failed: {result.error_message}")

    # Build DeepEval test case
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

    # Build metrics list based on test case configuration
    metrics = []

    # Get evaluator model for LLM-judged metrics
    evaluator = get_evaluator_model(eval_settings.evaluator_model)

    # Tool correctness metric
    tool_threshold = test_case.metrics.get(
        "tool_correctness", eval_settings.tool_correctness_threshold
    )
    metrics.append(ToolCorrectnessMetric(threshold=tool_threshold, model=evaluator))

    # Task completion metric
    task_threshold = test_case.metrics.get(
        "task_completion", eval_settings.task_completion_threshold
    )
    metrics.append(TaskCompletionMetric(threshold=task_threshold, model=evaluator))

    # Data plausibility metric (only for certain tests)
    if "data_plausibility" in test_case.metrics:
        metrics.append(
            create_data_plausibility_metric(network=test_case.network, model=evaluator)
        )

    # Resource discovery metric
    metrics.append(
        ResourceDiscoveryMetric(threshold=eval_settings.resource_discovery_threshold)
    )

    # Data source validation metric (if expected tables specified)
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

    # Run evaluation
    eval_results = evaluate(test_cases=[llm_test_case], metrics=metrics)

    # Record trace (with Langfuse score recording if enabled)
    trace_recorder.record(
        test_id=test_id,
        input_prompt=test_case.input,
        output=result.output,
        tool_calls=[
            {"name": tc.name, "input": tc.input, "result": tc.result}
            for tc in result.tool_calls
        ],
        metrics=[
            {
                "name": m.name,
                "score": m.score,
                "passed": m.success,
                "reason": m.reason,
            }
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

    # Flush Langfuse to ensure traces are sent
    agent.flush()

    # Check all metrics passed
    failed_metrics = [
        (r.name, r.score, r.reason)
        for r in eval_results.test_results[0].metrics_data
        if not r.success
    ]

    if failed_metrics:
        failure_msg = "\n".join(
            f"  - {name}: score={score:.2f}, reason={reason}"
            for name, score, reason in failed_metrics
        )
        pytest.fail(f"Metrics failed for {test_id}:\n{failure_msg}")


@pytest.mark.asyncio
async def test_basic_query_with_examples_search(
    agent: MCPAgent,
    eval_settings: EvalSettings,
) -> None:
    """Test that agent can use the unified search tool effectively."""
    prompt = "Search for examples of querying block data and then query the last 10 blocks on mainnet."

    result = await agent.execute(prompt)

    if result.is_error:
        pytest.fail(f"Agent execution failed: {result.error_message}")

    # Check that search was used with the examples type
    search_calls = [
        tc
        for tc in result.tool_calls
        if tc.name == "search" or tc.name.endswith("__search")
    ]
    assert search_calls, f"Expected search to be called, got: {[tc.name for tc in result.tool_calls]}"
    assert any(
        tc.input.get("type") == "examples" for tc in search_calls
    ), f"Expected search(type='examples'), got: {[tc.input for tc in search_calls]}"

    # Check that execute_python was also used
    tool_names = [tc.name for tc in result.tool_calls]
    assert any(
        "execute_python" in name for name in tool_names
    ), f"Expected execute_python to be called, got: {tool_names}"
