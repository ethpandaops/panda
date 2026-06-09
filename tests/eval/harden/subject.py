"""Subjects — the harness-agnostic agent runners.

A ``Subject`` runs a question and returns a normalized ``RunTrace``. This is the ONLY
place that knows about a specific coding-agent harness. opencode is implemented
today; adding Codex CLI / Claude Code / etc. is just another ``Subject`` that maps
that harness's output into a ``RunTrace`` — nothing downstream changes.

Telemetry: the underlying agent already ships its (high-fidelity) trace to Langfuse when
configured (LANGFUSE_* in the environment). That's the only Langfuse touch — humans inspect
those traces in Langfuse production; the harness scores/gates purely on the returned
``RunTrace``. ``flush()`` pushes any buffered trace before the process moves on; without
Langfuse keys it's a no-op and everything still works in-process.
"""

from __future__ import annotations

from typing import Protocol, runtime_checkable

from harden.trace import RunTrace, ToolCall


@runtime_checkable
class Subject(Protocol):
    """A runnable agent harness under evaluation. Implement ``run`` for a new harness.

    ``run`` takes the full turn sequence (a 1-element list for single-turn questions,
    longer for multi-turn) and returns one ``RunTrace`` aggregating the whole exchange.
    """

    name: str

    async def run(self, prompts: list[str]) -> RunTrace: ...


def _stringify_args(tool_call: object) -> str:
    inp = getattr(tool_call, "input", None)
    if isinstance(inp, dict):
        return str(inp.get("command") or inp.get("code") or inp)
    return str(inp)


def _tool_calls(results) -> list[ToolCall]:
    # Store the FULL raw output — capture-fidelity principle. The proposer prompt is
    # bounded downstream by report.py; we don't lose data here.
    return [
        ToolCall(
            name=tc.name,
            arguments=_stringify_args(tc),
            output=str(tc.result or ""),
            is_error=getattr(tc, "is_error", False),
            duration_ms=getattr(tc, "duration_ms", 0),
        )
        for r in results
        for tc in r.tool_calls
    ]


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

    async def run(self, prompts: list[str]) -> RunTrace:
        question = " ⟶ ".join(prompts)
        try:
            if len(prompts) == 1:
                results = [await self._agent.execute(prompts[0], test_id="harden")]
            else:
                # Reuse one session across turns so later prompts see earlier state.
                results = await self._agent.execute_multi_turn(prompts, test_id="harden")
        except Exception as exc:  # noqa: BLE001 - a crashed run is a 0-score datum, not a loop failure
            return RunTrace(
                question=question,
                subject=self.name,
                output="",
                crashed=True,
                error=f"{type(exc).__name__}: {exc}",
            )
        # Aggregate the turns. The grader only sees ``output``, so for a multi-turn run we
        # make it a TRANSCRIPT of every turn — otherwise a rubric that checks something
        # produced in turn 2 can't see it (only the last turn would survive). Each turn is
        # tagged with its session id so a rubric can verify session reuse (a stable id across
        # turns) directly from the text. Single-turn keeps the bare answer. Tool calls from
        # all turns are concatenated onto the on-disk trace; tokens/duration summed; trace
        # identity is the final turn's so a later record() lands on the right Langfuse trace.
        final = results[-1]
        output = (
            "\n\n".join(
                f"[Turn {i} | session={r.session_id or 'none'}] {prompt}\n"
                f"{(r.output or '').strip()}".rstrip()
                for i, (prompt, r) in enumerate(zip(prompts, results), start=1)
            )
            if len(results) > 1
            else (final.output or "")
        )
        return RunTrace(
            question=question,
            subject=self.name,
            output=output,
            tool_calls=_tool_calls(results),
            input_tokens=sum(r.input_tokens for r in results),
            output_tokens=sum(r.output_tokens for r in results),
            duration_ms=sum(r.duration_ms for r in results),
            crashed=any(r.is_error for r in results),
            error=next((r.error_message for r in results if r.is_error), None),
            trace_id=final.trace_id,
        )

    def flush(self) -> None:
        """Flush the agent's Langfuse client so traces land before the process moves on.
        Best-effort: no client -> no-op. Called by the provider after each run."""
        self._agent.flush()

    @property
    def session_id(self) -> str | None:
        """The Langfuse session all of this run's traces group under (None if disabled)."""
        return self._agent.langfuse_session_id

    def trace_url(self, trace_id: str | None) -> str | None:
        """Deep-link to a run's Langfuse trace, via the agent's configured client."""
        lf = self._agent.langfuse
        if lf is None or not trace_id:
            return None
        try:
            return lf.get_trace_url(trace_id=trace_id)
        except Exception:  # noqa: BLE001 - a link is best-effort, never fail a run over it
            return None
