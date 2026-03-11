"""Tests for multi-step sessions against ethpandaops-panda."""

from __future__ import annotations

from typing import TYPE_CHECKING

import pytest
from deepeval import evaluate
from deepeval.metrics import TaskCompletionMetric
from deepeval.test_case import LLMTestCase, ToolCall

from cases.loader import load_multi_step_cases
from config.evaluator import get_evaluator_model
from conftest import CostTracker
from metrics.visualization import VisualizationURLMetric

if TYPE_CHECKING:
    from agent.wrapper import MCPAgent
    from config.settings import EvalSettings


# Load test cases at module level for parametrization
_test_cases = load_multi_step_cases("multi_step.yaml")


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
@pytest.mark.slow
@pytest.mark.multi_step
@pytest.mark.parametrize("test_id", _get_test_ids())
async def test_multi_step_session(
    test_id: str,
    agent: MCPAgent,
    eval_settings: EvalSettings,
    cost_tracker: CostTracker,
) -> None:
    """Test a multi-step session with state persistence.

    Args:
        test_id: ID of the test case to run.
        agent: The MCPAgent instance.
        eval_settings: Evaluation settings.
        cost_tracker: Cost tracker for aggregating costs.
    """
    test_case = _get_test_case(test_id)

    session_id: str | None = None
    all_results = []
    total_cost = 0.0
    total_input_tokens = 0
    total_output_tokens = 0
    total_duration_ms = 0

    # Get evaluator model for LLM-judged metrics
    evaluator = get_evaluator_model(eval_settings.evaluator_model)

    for step_idx, step in enumerate(test_case.steps):
        # Use previous session if specified
        use_session = session_id if step.use_previous_session else None

        # Pass test_id with step number for Langfuse trace naming
        step_test_id = f"{test_id}-step-{step_idx + 1}"
        result = await agent.execute(step.prompt, session_id=use_session, test_id=step_test_id)
        all_results.append(result)

        # Accumulate costs
        total_cost += result.total_cost_usd or 0.0
        total_input_tokens += result.input_tokens
        total_output_tokens += result.output_tokens
        total_duration_ms += result.duration_ms

        if eval_settings.verbose:
            print(f"\n  Step {step_idx + 1}/{len(test_case.steps)}: {step.prompt[:50]}...")
            print(f"    Cost: ${result.total_cost_usd or 0:.6f}")
            print(f"    Tokens: {result.input_tokens} in / {result.output_tokens} out")
            print(f"    Tools: {[tc.name for tc in result.tool_calls]}")

        # Check for execution errors
        if result.is_error:
            pytest.fail(
                f"Step {step_idx + 1} failed for {test_id}: {result.error_message}"
            )

        # Capture session_id if expected
        if step.expect_session_id and result.session_id:
            session_id = result.session_id

        # Run step-specific metrics if defined
        if step.metrics:
            llm_test_case = LLMTestCase(
                input=step.prompt,
                actual_output=result.output,
                expected_tools=[ToolCall(name=t) for t in step.expected_tools],
                tools_called=[ToolCall(name=tc.name) for tc in result.tool_calls],
                additional_metadata={
                    "resources_read": result.resources_read,
                    "tool_calls": [
                        {"name": tc.name, "input": tc.input, "result": tc.result}
                        for tc in result.tool_calls
                    ],
                },
            )

            metrics = []

            if "task_completion" in step.metrics:
                metrics.append(
                    TaskCompletionMetric(
                        threshold=step.metrics["task_completion"],
                        model=evaluator,
                    )
                )

            if "visualization_url" in step.metrics:
                metrics.append(
                    VisualizationURLMetric(threshold=step.metrics["visualization_url"])
                )

            if metrics:
                eval_results = evaluate(test_cases=[llm_test_case], metrics=metrics)

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
                    pytest.fail(
                        f"Step {step_idx + 1} metrics failed for {test_id}:\n{failure_msg}"
                    )

    # Record aggregate cost for the whole session
    cost_tracker.record(
        test_id=test_id,
        model=eval_settings.model,
        input_tokens=total_input_tokens,
        output_tokens=total_output_tokens,
        cost_usd=total_cost,
        duration_ms=total_duration_ms,
    )

    # Flush Langfuse to ensure traces are sent
    agent.flush()

    if eval_settings.verbose:
        print(f"\n  Session {test_id} complete:")
        print(f"    Total Cost: ${total_cost:.6f}")
        print(f"    Total Tokens: {total_input_tokens} in / {total_output_tokens} out")
        print(f"    Total Duration: {total_duration_ms}ms")
        print(f"    Steps: {len(test_case.steps)}")


@pytest.mark.asyncio
@pytest.mark.slow
async def test_session_persistence(
    agent: MCPAgent,
    eval_settings: EvalSettings,
) -> None:
    """Test that session state persists across multiple turns."""
    prompts = [
        "Create a variable called 'test_data' containing a list of numbers from 1 to 10.",
        "Double each number in 'test_data' and store it in 'doubled_data'.",
        "Calculate the sum of 'doubled_data' and print it.",
    ]

    results = await agent.execute_multi_turn(prompts, test_id="session_persistence")

    # Flush Langfuse to ensure traces are sent
    agent.flush()

    # Should have results for all prompts
    assert len(results) == len(prompts), "Expected result for each prompt"

    # No errors
    for i, result in enumerate(results):
        if result.is_error:
            pytest.fail(f"Turn {i + 1} failed: {result.error_message}")

    # Final output should contain the sum (110)
    final_output = results[-1].output.lower()
    assert "110" in final_output or "sum" in final_output, (
        f"Expected sum to be in output, got: {final_output[:200]}"
    )
