"""Shared result types for the eval agent."""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any


@dataclass
class ToolCallRecord:
    """Record of a single tool call."""

    name: str
    input: dict[str, Any]
    result: Any | None = None
    duration_ms: int = 0
    is_error: bool = False


@dataclass
class ExecutionResult:
    """Result of agent execution with metrics."""

    output: str
    tool_calls: list[ToolCallRecord] = field(default_factory=list)
    resources_read: list[str] = field(default_factory=list)
    total_cost_usd: float | None = None
    input_tokens: int = 0
    output_tokens: int = 0
    duration_ms: int = 0
    num_turns: int = 0
    session_id: str | None = None
    is_error: bool = False
    error_message: str | None = None
    # Langfuse trace identity for THIS run, stamped on the result so callers read it
    # off the returned object rather than a mutable agent property after later awaits.
    trace_id: str | None = None
