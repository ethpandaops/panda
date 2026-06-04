"""Agent module for ethpandaops-panda evaluation."""

from __future__ import annotations

from typing import TYPE_CHECKING

from agent.wrapper import ExecutionResult, MCPAgent

if TYPE_CHECKING:
    from config.settings import EvalSettings


def make_agent(settings: EvalSettings):
    """Build the agent backend selected by ``settings.agent_api``.

    - ``opencode``: drives ``opencode serve`` via the opencode SDK (default).
    - ``openai``: OpenAI-class endpoint (e.g. OpenRouter) with a native MCP tool loop.
    - ``anthropic``: Claude Agent SDK (Anthropic-class endpoint).
    """
    if settings.agent_api == "opencode":
        from agent.opencode_agent import OpenCodeAgent

        return OpenCodeAgent(settings)

    if settings.agent_api == "openai":
        from agent.openai_agent import OpenAIAgent

        return OpenAIAgent(settings)

    return MCPAgent(settings)


__all__ = ["ExecutionResult", "MCPAgent", "make_agent"]
