"""Normalized run trace — the harness-agnostic contract.

Everything downstream (scoring, runner, the future optimizer) speaks ``RunTrace``,
never a specific harness's native types. To support another coding-agent harness as
a subject you implement one thing: produce a ``RunTrace`` from it (see subject.py).
That single seam is what keeps the whole system harness-agnostic.

Design choice: store the RAW content of every step (name / arguments / output /
is_error) and nothing pre-digested. The optimizer reasons over the raw trace; the
loop decides if a run was good from TOP-LEVEL metrics only (correctness + token
efficiency, see scoring.py). We deliberately do NOT parse panda-specific structure
(which datasource/table/SQL a call used) out of the steps — that would be brittle,
harness-specific, and the raw content already carries it for any model to read.
"""

from __future__ import annotations

from dataclasses import dataclass, field

# Separates the agent's answer from the harness-captured tool calls in the text the
# grader judges. The provider strips any occurrence of it from the agent's own answer
# before appending the real section, so an answer can't forge tool-call "evidence" —
# everything after the (single) marker is ground truth captured by the harness.
TOOLS_MARKER = "--- tool calls the agent made to reach this answer (harness-captured) ---"


@dataclass
class ToolCall:
    """One tool invocation within a run, normalized across harnesses.

    Stores RAW signal only: the step's name, its full arguments and output, whether it
    errored, and how long it took. No interpretation — the raw content is the surface
    the optimizer reasons over.
    """

    name: str
    arguments: str  # the command / code / args, stringified — RAW, full
    output: str  # the tool's result/output — RAW
    is_error: bool = False
    duration_ms: int = 0


@dataclass
class RunTrace:
    """One execution of one question by one subject (harness + model).

    The atomic unit of measurement. Scoring, aggregation, and the optimizer all
    consume this and nothing harness-specific.
    """

    question: str
    subject: str  # stable id, e.g. "opencode:gpt-5.4-mini:cli"
    output: str  # the agent's final answer
    tool_calls: list[ToolCall] = field(default_factory=list)
    input_tokens: int = 0
    output_tokens: int = 0
    duration_ms: int = 0
    crashed: bool = False  # harness/agent errored before producing an answer
    error: str | None = None
    # Telemetry identity of THIS run, captured immutably at run time (None if no trace
    # was pushed). Carried here — not read off a mutable subject property — so it attaches
    # to the right trace even under concurrent runs.
    trace_id: str | None = None
    trace_url: str | None = None  # deep-link to the Langfuse trace (None if not pushed)

    @property
    def n_tools(self) -> int:
        return len(self.tool_calls)

    @property
    def n_tool_errors(self) -> int:
        return sum(1 for t in self.tool_calls if t.is_error)

    @property
    def total_tokens(self) -> int:
        return self.input_tokens + self.output_tokens
