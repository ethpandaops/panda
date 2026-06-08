"""Correctness judge — the one subjective call in the loop, isolated here.

Grading has two modes, picked per case:

- REFERENCE grading (when the case has a ground truth): the answer is scored for factual
  consistency against ground truth — either a literal ``reference`` (static facts) or the
  result of a ``reference_query`` the judge RUNS at grade time (canonical SQL, so the
  truth is current and never drifts). A confident wrong answer fails. This is what makes
  "correctness" mean correct, not just plausible.
- TASK-COMPLETION grading (no reference): falls back to "did the agent complete the task?"
  — fine for live/unverifiable questions (a CPU number, "list the datasources"), weak for
  analytical ones, which is exactly why those should carry a reference_query.

This is the only LLM call on the scoring path; everything else in harden is deterministic.
"""

from __future__ import annotations

import asyncio
import subprocess
from dataclasses import dataclass

from harden.trace import RunTrace


@dataclass
class Verdict:
    correct: bool
    correctness: float  # judge's raw 0..1 score
    reason: str


class Judge:
    """Grades a ``RunTrace`` — against ground truth when the case provides one, else for
    task completion via the shared evaluator model."""

    def __init__(self, evaluator_model: str, *, threshold: float = 0.7) -> None:
        self.evaluator_model = evaluator_model
        self.threshold = threshold
        self._ref_cache: dict[tuple[str, str], str] = {}

    async def judge(
        self,
        trace: RunTrace,
        *,
        reference: str = "",
        reference_query: str = "",
        reference_query_datasource: str = "clickhouse-refined",
    ) -> Verdict:
        if trace.crashed or not trace.output:
            return Verdict(correct=False, correctness=0.0, reason=trace.error or "no output")

        ground_truth = await self._ground_truth(
            reference, reference_query, reference_query_datasource
        )
        if ground_truth:
            return await self._grade_against(trace, ground_truth)
        return await self._grade_task_completion(trace)

    async def _ground_truth(self, reference: str, query: str, datasource: str) -> str:
        if reference:
            return reference
        if not query:
            return ""
        key = (datasource, query)
        if key in self._ref_cache:
            return self._ref_cache[key]

        # Run the canonical query via the panda CLI (same server/config the subjects use).
        # Off-thread so the concurrent run loop isn't blocked; result cached per (ds, sql).
        def run() -> str:
            try:
                p = subprocess.run(
                    ["panda", "clickhouse", "query", datasource, query],
                    text=True,
                    capture_output=True,
                    timeout=120,
                )
                return (p.stdout or "").strip() if p.returncode == 0 else ""
            except Exception:  # noqa: BLE001 - a failed reference query just means no ground truth
                return ""

        val = await asyncio.to_thread(run)
        self._ref_cache[key] = val
        return val

    async def _grade_against(self, trace: RunTrace, ground_truth: str) -> Verdict:
        from deepeval.metrics import GEval
        from deepeval.test_case import LLMTestCase, LLMTestCaseParams

        from config.evaluator import get_evaluator_model

        metric = GEval(
            name="Correctness",
            criteria=(
                "EXPECTED OUTPUT is ground truth computed directly from the data. Score how "
                "factually consistent the ACTUAL OUTPUT is with it: the key figures/entities "
                "must agree (allow rounding, units, formatting, and extra explanation). A "
                "confident answer that disagrees with the expected output must score low."
            ),
            evaluation_params=[
                LLMTestCaseParams.INPUT,
                LLMTestCaseParams.ACTUAL_OUTPUT,
                LLMTestCaseParams.EXPECTED_OUTPUT,
            ],
            model=get_evaluator_model(self.evaluator_model),
            threshold=self.threshold,
        )
        ltc = LLMTestCase(
            input=trace.question, actual_output=trace.output, expected_output=ground_truth
        )
        try:
            await metric.a_measure(ltc, _show_indicator=False)
        except Exception as exc:  # noqa: BLE001 - a judge hiccup scores 0, never crashes the loop
            return Verdict(correct=False, correctness=0.0, reason=f"judge error: {exc}")
        score = float(metric.score or 0.0)
        return Verdict(
            correct=score >= self.threshold, correctness=score, reason=metric.reason or ""
        )

    async def _grade_task_completion(self, trace: RunTrace) -> Verdict:
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
        except Exception as exc:  # noqa: BLE001
            return Verdict(correct=False, correctness=0.0, reason=f"judge error: {exc}")
        score = float(metric.score or 0.0)
        return Verdict(
            correct=score >= self.threshold, correctness=score, reason=metric.reason or ""
        )
