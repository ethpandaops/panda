"""Turn measured runs into the proposer's prompt — LEAN summary + on-demand full traces.

The proposer is a coding agent with file access, so we don't dump every run's full
step-by-step content into its context (that's huge and wasteful). Instead the prompt is a
compact per-run summary — ranked by the top-level metrics only (wrong first, then most
wasteful) — plus the path to the FULL untruncated traces on disk, which the proposer reads
for the runs it cares about. We pick WHICH runs to surface by score, but we never
pre-judge WHY (no "wrong datasource"/"error 277" labels) — that's the overfit trap; the
model reads the raw trace and decides.
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

An auditor will be checking your changes for multiple criteria, like if the solution is
overfit, or if it is placed in the wrong location, or if it's going to sacrifice
effectiveness and efficiency in general. "Cheating" won't work, your solution MUST
generalize and be sustainable for the long term. Concerns need to be seperated.
"""


def worst_records(records: list[RunRecord], limit: int) -> list[RunRecord]:
    """The runs most worth showing the proposer: wrong ones first, then the most
    wasteful (lowest score) — ranked purely on the top-level metrics."""
    ranked = sorted(records, key=lambda r: (r.score.correct, r.score.score))
    return ranked[:limit]


def summarize_record(record: RunRecord) -> str:
    """One run as a COMPACT line: metrics + the tool-call shape + final answer. No raw
    outputs — the proposer reads the full trace file if it wants the detail."""
    rs, trace = record.score, record.trace
    if trace.crashed:
        return (
            f"- {record.question.id} [{trace.subject}] CRASHED: {trace.error} "
            f"(score {rs.score:.2f})"
        )
    shape = " → ".join(f"{tc.name}{'!' if tc.is_error else ''}" for tc in trace.tool_calls) or "—"
    answer = " ".join((trace.output or "").split())[:200]
    return (
        f"- {record.question.id} [{trace.subject}] correct={rs.correct} tokens={rs.tokens} "
        f"tools={rs.n_tools} score={rs.score:.2f}\n"
        f"    steps: {shape}\n"
        f"    answer: {answer}"
    )


def build_proposal_prompt(
    records: list[RunRecord], *, traces_dir: str | None = None, limit: int = 12
) -> str:
    """The prompt handed to the proposer: objective + a lean summary of the worst runs +
    a pointer to the full traces on disk (read on demand)."""
    shown = worst_records(records, limit)
    body = "\n".join(summarize_record(r) for r in shown)
    pointer = ""
    if traces_dir:
        pointer = (
            f"\n\nThe FULL step-by-step trace for every run (complete tool inputs + outputs, "
            f"untruncated) is on disk in:\n  {traces_dir}\n"
            "Read the trace files for the runs above before proposing — that's where you see "
            "exactly what the agent ran and what came back. One file per run."
        )
    return f"{_OBJECTIVE}\nRuns (worst first):\n{body}{pointer}"
