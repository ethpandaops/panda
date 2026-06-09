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
from harden.subject import OpencodeSubject

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


def call_api(prompt, options, context):
    cfg = (options or {}).get("config", {}) or {}
    # The first turn is the rendered prompt; extra turns come from the `followups` var
    # (run in one session). Single-turn questions have no followups.
    followups = _followups((context or {}).get("vars") or {})
    prompts = [prompt, *[str(f) for f in followups]]
    subject = _subject(cfg)
    trace = asyncio.run(subject.run(prompts))
    subject.flush()  # push the run's Langfuse trace before promptfoo moves to the next
    return {
        "output": trace.output or "",
        "tokenUsage": {
            "total": trace.total_tokens,
            "prompt": trace.input_tokens,
            "completion": trace.output_tokens,
        },
        # Full raw trace — every step's complete arguments + output, nothing truncated.
        "metadata": {
            "subject": trace.subject,
            "crashed": trace.crashed,
            "error": trace.error,
            "trace_id": trace.trace_id,
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
