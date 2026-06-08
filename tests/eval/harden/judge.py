"""Correctness judge — the one subjective call in the loop, isolated here.

Reuses the existing eval judge (DeepEval ``TaskCompletionMetric`` over the configured
evaluator model) so harden and the pytest suite grade "did it answer the question?"
identically. Everything else in harden is deterministic; this is the only LLM call on
the scoring path, so it lives behind one small seam that's easy to swap or stub.
"""

from __future__ import annotations

from dataclasses import dataclass

from harden.trace import RunTrace


@dataclass
class Verdict:
    correct: bool
    correctness: float  # judge's raw 0..1 score
    reason: str


class Judge:
    """Grades a ``RunTrace`` for task completion via the shared evaluator model."""

    def __init__(self, evaluator_model: str, *, threshold: float = 0.7) -> None:
        self.evaluator_model = evaluator_model
        self.threshold = threshold

    async def judge(self, trace: RunTrace) -> Verdict:
        if trace.crashed or not trace.output:
            return Verdict(correct=False, correctness=0.0, reason=trace.error or "no output")

        from deepeval.metrics import TaskCompletionMetric
        from deepeval.test_case import LLMTestCase
        from deepeval.test_case import ToolCall as DToolCall

        from config.evaluator import get_evaluator_model

        ltc = LLMTestCase(
            input=trace.question,
            actual_output=trace.output,
            tools_called=[DToolCall(name=tc.name) for tc in trace.tool_calls],
        )
        metric = TaskCompletionMetric(
            threshold=self.threshold, model=get_evaluator_model(self.evaluator_model)
        )
        try:
            await metric.a_measure(ltc, _show_indicator=False)
        except Exception as exc:  # noqa: BLE001 - a judge hiccup scores 0, never crashes the loop
            return Verdict(correct=False, correctness=0.0, reason=f"judge error: {exc}")
        score = float(metric.score or 0.0)
        return Verdict(
            correct=score >= self.threshold, correctness=score, reason=metric.reason or ""
        )
