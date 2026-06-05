"""Agent module for ethpandaops-panda evaluation (opencode backend)."""

from __future__ import annotations

from typing import TYPE_CHECKING

from agent.wrapper import ExecutionResult

if TYPE_CHECKING:
    from config.settings import EvalSettings


def make_agent(settings: EvalSettings):
    """Build the opencode agent backend (drives ``opencode serve`` via the SDK)."""
    from agent.opencode_agent import OpenCodeAgent

    return OpenCodeAgent(settings)


__all__ = ["ExecutionResult", "make_agent"]
