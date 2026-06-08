"""Subjects — the harness-agnostic agent runners.

A ``Subject`` runs a question and returns a normalized ``RunTrace``. This is the ONLY
place that knows about a specific coding-agent harness. opencode is implemented
today; adding Codex CLI / Claude Code / etc. is just another ``Subject`` that maps
that harness's output into a ``RunTrace`` — nothing downstream changes.

Telemetry: the underlying agent already ships its (now high-fidelity) trace to Langfuse
when configured. Attaching harden's objective scores to that trace is also a
harness-specific concern (it needs the harness's Langfuse client), so it lives here in
``record()`` — the generic runner never touches Langfuse. ``record()`` is optional: a
subject without it (or without Langfuse keys) simply scores in-process, and the loop's
gating is unaffected since it runs on the returned ``RunTrace``/scores.
"""

from __future__ import annotations

from typing import Protocol, runtime_checkable

from harden.trace import RunTrace, ToolCall


@runtime_checkable
class Subject(Protocol):
    """A runnable agent harness under evaluation. Implement ``run`` for a new harness."""

    name: str

    async def run(self, question: str) -> RunTrace: ...


def _stringify_args(tool_call: object) -> str:
    inp = getattr(tool_call, "input", None)
    if isinstance(inp, dict):
        return str(inp.get("command") or inp.get("code") or inp)
    return str(inp)


class OpencodeSubject:
    """Runs a question through the opencode harness for a given model + route.

    Wraps the existing ``OpenCodeAgent``. ``langfuse_enabled`` is honored from the
    environment via settings, so when Langfuse keys are present every run is pushed
    there automatically; when they're absent the subject still works (in-process only).
    """

    def __init__(
        self,
        model: str,
        route: str = "cli",
        *,
        evaluator_model: str | None = None,
        timeout: float = 120.0,
    ) -> None:
        from agent.opencode_agent import OpenCodeAgent
        from config.settings import EvalSettings

        settings = EvalSettings()
        settings.opencode_route = route
        settings.model = model
        settings.opencode_timeout = timeout
        if evaluator_model:
            settings.evaluator_model = evaluator_model

        self.settings = settings
        self.name = f"opencode:{model}:{route}"
        self._agent = OpenCodeAgent(settings)

    async def run(self, question: str) -> RunTrace:
        try:
            result = await self._agent.execute(question, test_id="harden")
        except Exception as exc:  # noqa: BLE001 - a crashed run is a 0-score datum, not a loop failure
            return RunTrace(
                question=question,
                subject=self.name,
                output="",
                crashed=True,
                error=f"{type(exc).__name__}: {exc}",
            )
        # Capture the trace identity NOW (immutably onto the RunTrace) so a later
        # record() lands on this run's trace even if the agent has since run again.
        return RunTrace(
            question=question,
            subject=self.name,
            output=result.output or "",
            tool_calls=[
                ToolCall(
                    name=tc.name,
                    arguments=_stringify_args(tc),
                    output=str(tc.result or "")[:4000],
                    is_error=getattr(tc, "is_error", False),
                    duration_ms=getattr(tc, "duration_ms", 0),
                )
                for tc in result.tool_calls
            ],
            input_tokens=result.input_tokens,
            output_tokens=result.output_tokens,
            duration_ms=result.duration_ms,
            crashed=result.is_error,
            error=result.error_message,
            trace_id=result.trace_id,
        )

    def record(
        self,
        trace: RunTrace,
        scores: dict[str, float],
        *,
        comment: str = "",
        category: str | None = None,
        branch: str | None = None,
        run_name: str | None = None,
        question_id: str | None = None,
        question_text: str | None = None,
    ) -> None:
        """Attach harden's objective as native scores on this run's Langfuse trace, and
        link it into the per-branch dataset run. Best-effort: no client / no trace id ->
        no-op; a Langfuse hiccup is logged, never raised into the loop."""
        langfuse = self._agent.langfuse
        trace_id = trace.trace_id
        if langfuse is None or not trace_id:
            return
        try:
            for name, value in scores.items():
                langfuse.create_score(
                    trace_id=trace_id,
                    name=name,
                    value=float(value),
                    comment=comment if name == "correctness" and comment else None,
                )
        except Exception as exc:  # noqa: BLE001 - scoring is best-effort
            print(f"  [langfuse] score push failed: {type(exc).__name__}: {exc}")
            return

        if category and branch and run_name and question_id:
            try:
                from langfuse_dataset import link_run, upsert_item

                upsert_item(
                    langfuse,
                    category=category,
                    branch=branch,
                    case_id=question_id,
                    input_text=question_text or "",
                )
                link_run(
                    langfuse,
                    category=category,
                    branch=branch,
                    case_id=question_id,
                    run_name=run_name,
                    trace_id=trace_id,
                )
            except Exception as exc:  # noqa: BLE001 - dataset linking is best-effort
                print(f"  [langfuse] dataset link failed: {type(exc).__name__}: {exc}")

    def flush(self) -> None:
        self._agent.flush()
