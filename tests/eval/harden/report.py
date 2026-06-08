"""Turn measured runs into the proposer's prompt — a RAW dump, nothing digested.

The proposer is a coding agent; its reasoning surface is the full step-by-step content
of the agent runs, exactly as captured. We pick WHICH runs to show by the top-level
metrics only (wrong first, then most wasteful), and we hand over the raw steps. We do
NOT summarize "it used the wrong datasource" or "error code 277" — that pre-judging is
what makes a harness optimizer overfit. The model reads the raw trace and decides.
"""

from __future__ import annotations

from harden.runner import RunRecord

_OBJECTIVE = """\
You are improving the `panda` CLI + MCP harness so AI agents answer questions about the
ethPandaOps Ethereum data ecosystem more RELIABLY (correct answers) and more EFFICIENTLY
(far fewer wasted steps/tokens). You may edit anything in the panda source that shapes
how an agent experiences the tool: CLI command help and output, MCP tool descriptions,
error messages, the Python sandbox API, docs, examples, and runbooks — and real bugs.

Hard rules (these are gated — violations are reverted, so a "win" that breaks one is wasted work):
- Do NOT encode the ANSWER to the questions below. Putting a specific table, column,
  or query pattern that one of these questions needs into the harness is leakage, not a
  fix — even if it's dressed as a general example or error hint. If you find yourself
  writing `seen_slot_start_diff`, a specific table name, or "for latest-X questions do Y"
  where Y is one of these questions' answers, stop.
- PLACEMENT matters. Dataset-specific knowledge (which table/column holds what for a
  given dataset) belongs in that DATASOURCE's searchable examples/docs/schema, fetched on
  demand — NEVER in a generic module's always-loaded description or in error-hint text.
  Error hints must be error-CLASS generic: explain the error and how to DISCOVER the fix
  (e.g. "filter on the table's primary key; run `panda schema <…>` to see it") and name
  no dataset-specific columns or tables. Anything always-loaded is paid for by every
  question, so it must help broadly or it's bloat.
- Do NOT change product behavior to relieve pressure the TEST creates. Session lifecycle,
  execution semantics, timeouts, resource limits, retry counts — if a change only helps
  because the eval runs many failing attempts, that's gaming the harness, not improving
  it. Those are test-config knobs. Fix the agent's experience, not the test's plumbing.
- Prefer fixing the ROOT cause an agent tripped on (a confusing error, a missing hint,
  a wrong default, a real bug) over adding narrow guidance. Keep edits minimal. Do not
  touch the eval harness (tests/eval/**).

Below are real agent runs: the question, the full raw trace (every tool call's input and
output), the final answer, whether it was correct, and the tokens it burned. Study where
agents flailed, then make targeted harness edits.
"""


def worst_records(records: list[RunRecord], limit: int) -> list[RunRecord]:
    """The runs most worth showing the proposer: wrong ones first, then the most
    wasteful (lowest score) — ranked purely on the top-level metrics."""
    ranked = sorted(records, key=lambda r: (r.score.correct, r.score.score))
    return ranked[:limit]


def format_record(record: RunRecord, *, max_output_chars: int = 2000) -> str:
    """One run as raw text: header metrics, then each step's raw input -> output."""
    rs, trace = record.score, record.trace
    head = (
        f"### {record.question.id} — {record.question.text}\n"
        f"subject={trace.subject} correct={rs.correct} tokens={rs.tokens} "
        f"tools={rs.n_tools} score={rs.score:.2f}"
    )
    if trace.crashed:
        return f"{head}\nCRASHED: {trace.error}\n"
    lines = [head, "steps:"]
    for i, tc in enumerate(trace.tool_calls, 1):
        out = (tc.output or "").strip()
        if len(out) > max_output_chars:
            out = out[:max_output_chars] + f"… [+{len(out) - max_output_chars} chars]"
        err = " [ERROR]" if tc.is_error else ""
        lines.append(f"  {i}. {tc.name}{err}: {tc.arguments}")
        lines.append(f"     -> {out}")
    lines.append(f"final answer: {(trace.output or '').strip()}")
    return "\n".join(lines) + "\n"


def build_proposal_prompt(
    records: list[RunRecord], *, limit: int = 12, max_output_chars: int = 2000
) -> str:
    """The full prompt handed to the proposer: objective + raw worst runs."""
    shown = worst_records(records, limit)
    body = "\n".join(format_record(r, max_output_chars=max_output_chars) for r in shown)
    return f"{_OBJECTIVE}\n{body}"
