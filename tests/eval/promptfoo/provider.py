"""promptfoo custom provider that runs an ethpandaops agent (opencode/codex) as the model.

promptfoo owns the cases + asserts + grading; this is the bridge to our agentic subject.
The rendered prompt is a JSON list of turns (one element = single-turn), so multi-turn
runs in one session. The FULL raw trace is returned in ``metadata`` — untruncated — so the
harden loop has everything; what reaches the proposer's context is bounded later, in the
loop, not here.

NB: the provider ``config.timeout`` is promptfoo's WORKER timeout (ms). The subject's own
timeout is ``config.subject_timeout`` (seconds), kept separate to avoid the clash.
"""

import asyncio
import json
import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))  # tests/eval on path

from config.settings import DEFAULT_AGENT_MODEL, DEFAULT_AGENT_ROUTE
from harness.subject import OpencodeSubject
from harness.trace import TOOLS_MARKER

_SUBJECTS: dict[tuple, OpencodeSubject] = {}


def _subject(cfg: dict) -> OpencodeSubject:
    key = (cfg.get("model", DEFAULT_AGENT_MODEL), cfg.get("route", DEFAULT_AGENT_ROUTE))
    if key not in _SUBJECTS:
        _SUBJECTS[key] = OpencodeSubject(
            model=key[0], route=key[1], timeout=cfg.get("subject_timeout", 300)
        )
    return _SUBJECTS[key]


def _followups(vars_: dict) -> list:
    """Decode the `followups` var. It's JSON-encoded by the config (so promptfoo doesn't
    expand a raw list into a test matrix); tolerate a raw list too, for safety."""
    raw = vars_.get("followups")
    if isinstance(raw, str):
        raw = json.loads(raw) if raw.strip() else []
    return list(raw or [])


def _clip(text: str, limit: int) -> str:
    """Whitespace-flatten and bound `text`, keeping the head AND the tail when over
    budget. The decisive part of a tool output often sits at the end (`a || b` fallback
    chains where the error prints first, query results below a banner), so head-only
    truncation can destroy exactly the evidence the grader needs. The elision marker
    tells the grader content was cut, not that the tool returned nothing."""
    s = " ".join((text or "").split())
    if len(s) <= limit:
        return s
    head, tail = s[: limit * 2 // 3], s[-(limit // 3) :]
    return f"{head} …[{len(s) - len(head) - len(tail)} chars elided]… {tail}"


def _graded_output(trace) -> str:
    """What the grader judges: the agent's answer PLUS the tool calls it actually made to
    reach it. Those calls are harness-captured ground truth (the real query/command + its
    result), NOT the agent's self-report — so a rubric can verify the answer was sourced
    from a real query (which datasource, which table) rather than hallucinated, and the
    agent can't game it by merely claiming it queried. Args/results are clipped (head+tail)
    to keep the grading prompt bounded; the full untruncated trace is still in
    `metadata`/on disk.

    Anti-forgery: any imitation of the marker inside the agent's own answer is stripped,
    so the grader can rely on "everything after the marker is harness ground truth" —
    an answer can't smuggle in fake tool-call evidence (or fake grading instructions
    formatted as ours)."""
    answer = (trace.output or "").replace(TOOLS_MARKER, "[stripped: harness marker]")
    if not trace.tool_calls:
        return answer
    lines = [answer, "", TOOLS_MARKER]
    for t in trace.tool_calls:
        arg = _clip(t.arguments, 1200)
        res = _clip(t.output, 2400)
        mark = " [ERROR]" if t.is_error else ""
        line = f"- {t.name}{mark}: {arg}"
        if res:
            line += f"  ->  {res}"
        lines.append(line)
    return "\n".join(lines)


def call_api(prompt, options, context):
    cfg = (options or {}).get("config", {}) or {}
    # The first turn is the rendered prompt; extra turns come from the `followups` var
    # (run in one session). Single-turn questions have no followups.
    followups = _followups((context or {}).get("vars") or {})
    prompts = [prompt, *[str(f) for f in followups]]
    subject = _subject(cfg)
    trace = asyncio.run(subject.run(prompts))
    retried = False
    if trace.crashed:
        # One retry on a crash: crashes are dominated by transient infra (model
        # timeouts under load, provider 5xx) that contaminates the measurement — a
        # crash the HARNESS causes reproduces on the retry and still scores 0.
        retried = True
        trace = asyncio.run(subject.run(prompts))
    subject.flush()  # push the run's Langfuse trace before promptfoo moves to the next
    # Sandbox sessions are reaped server-side by the idle TTL (pkg/sandbox/session.go);
    # no per-run teardown here. A leaked single-shot session self-reaps once idle.
    return {
        "output": _graded_output(trace),
        "tokenUsage": {
            "total": trace.total_tokens,
            "prompt": trace.input_tokens,
            "completion": trace.output_tokens,
        },
        # Full raw trace — every step's complete arguments + output, nothing truncated.
        # `answer` is the clean answer (no tool appendix) so downstream reporting/disk stay
        # tidy while the grader still judges the answer-plus-tools `output` above.
        "metadata": {
            "answer": trace.output or "",
            "subject": trace.subject,
            "crashed": trace.crashed,
            "error": trace.error,
            "trace_id": trace.trace_id,
            "trace_url": subject.trace_url(trace.trace_id),
            "session_id": subject.session_id,
            "retried": retried,
            "input_tokens": trace.input_tokens,
            "output_tokens": trace.output_tokens,
            "duration_ms": trace.duration_ms,
            "tool_calls": [
                {
                    "name": t.name,
                    "arguments": t.arguments,
                    "output": t.output,
                    "is_error": t.is_error,
                    "duration_ms": t.duration_ms,
                }
                for t in trace.tool_calls
            ],
        },
    }
