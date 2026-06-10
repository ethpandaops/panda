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
import re
import sys
import urllib.request

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))  # tests/eval on path

import yaml

from config.settings import DEFAULT_AGENT_MODEL, DEFAULT_AGENT_ROUTE
from harden.subject import OpencodeSubject
from harden.trace import TOOLS_MARKER

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


def _graded_output(trace) -> str:
    """What the grader judges: the agent's answer PLUS the tool calls it actually made to
    reach it. Those calls are harness-captured ground truth (the real query/command + its
    result), NOT the agent's self-report — so a rubric can verify the answer was sourced
    from a real query (which datasource, which table) rather than hallucinated, and the
    agent can't game it by merely claiming it queried. Args/results are truncated to keep
    the grading prompt bounded; the full untruncated trace is still in `metadata`/on disk.

    Anti-forgery: any imitation of the marker inside the agent's own answer is stripped,
    so the grader can rely on "everything after the marker is harness ground truth" —
    an answer can't smuggle in fake tool-call evidence (or fake grading instructions
    formatted as ours)."""
    answer = (trace.output or "").replace(TOOLS_MARKER, "[stripped: harness marker]")
    if not trace.tool_calls:
        return answer
    lines = [answer, "", TOOLS_MARKER]
    for t in trace.tool_calls:
        arg = " ".join((t.arguments or "").split())[:600]
        res = " ".join((t.output or "").split())[:240]
        mark = " [ERROR]" if t.is_error else ""
        line = f"- {t.name}{mark}: {arg}"
        if res:
            line += f"  ->  {res}"
        lines.append(line)
    return "\n".join(lines)


# Panda sandbox session ids are 12-char hex. We only count an id as THIS agent's if it
# appears next to a session keyword in the agent's own commands, or as a `session_id`
# in a command's response — and never from `session list` output, which shows OTHER
# agents' sessions too.
_SESSION_IN_ARGS = re.compile(r"session[^\n]{0,40}?\b([0-9a-f]{12})\b", re.IGNORECASE)
_SESSION_IN_OUTPUT = re.compile(r"session_id[\"'\s:=]{1,5}([0-9a-f]{12})", re.IGNORECASE)
_LIST_CMD = re.compile(r"session(s\b|\s+list)", re.IGNORECASE)


def _server_base(subject) -> str:
    """The base URL of the panda server THIS run talked to: the CLI route resolves it
    from PANDA_CONFIG (the harden scratch config), else the subject's mcp_url."""
    cfg_path = os.environ.get("PANDA_CONFIG", "")
    if cfg_path and os.path.exists(cfg_path):
        try:
            with open(cfg_path) as f:
                cfg = yaml.safe_load(f) or {}
            base = (cfg.get("server") or {}).get("base_url")
            if base:
                return str(base).rstrip("/")
        except Exception:  # noqa: BLE001 - fall through to the default
            pass
    return str(subject.settings.mcp_url).rstrip("/")


def _teardown_agent_sessions(subject, trace) -> list[str]:
    """Destroy the sandbox sessions THIS agent created/used, now that its run is over.
    Per-agent by construction: only ids from this run's own transcript — concurrent
    agents' sessions (or a human's) are never touched. Best-effort; already-gone is fine."""
    ids: set[str] = set()
    for t in trace.tool_calls:
        ids.update(_SESSION_IN_ARGS.findall(t.arguments or ""))
        if not _LIST_CMD.search(t.arguments or ""):
            ids.update(_SESSION_IN_OUTPUT.findall(t.output or ""))
    if not ids:
        return []
    base = _server_base(subject)
    destroyed = []
    for sid in sorted(ids):
        req = urllib.request.Request(f"{base}/api/v1/sessions/{sid}", method="DELETE")
        try:
            with urllib.request.urlopen(req, timeout=10):  # noqa: S310
                destroyed.append(sid)
        except Exception:  # noqa: BLE001 - not a session / already gone
            continue
    return destroyed


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
    destroyed = _teardown_agent_sessions(subject, trace)  # this agent's sandboxes only
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
            "destroyed_sandbox_sessions": destroyed,
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
