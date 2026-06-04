"""OpenAI-class agent backend for ethpandaops-panda evaluation.

Drives the model under test through an OpenAI-compatible chat-completions endpoint
(e.g. OpenRouter) running its own tool-calling loop against panda's MCP tools. This
is the counterpart to the Anthropic-class backend in ``wrapper.py`` (Claude Agent
SDK), and is what lets a non-Claude model such as deepseek be the model under test
without any translation gateway.
"""

from __future__ import annotations

import json
import os
import time
from typing import TYPE_CHECKING, Any

from mcp import ClientSession
from mcp.client.sse import sse_client
from openai import AsyncOpenAI

from agent.wrapper import ExecutionResult, ToolCallRecord

if TYPE_CHECKING:
    from config.settings import EvalSettings

DEFAULT_OPENAI_BASE_URL = "https://openrouter.ai/api/v1"

# Use the bare panda MCP tool names (execute_python / search / manage_session) as
# OpenAI function names so cases and metrics match consistently across backends.
MCP_TOOL_PREFIX = ""

SYSTEM_PROMPT = (
    "You are an analytics agent for the ethpandaops 'panda' server, which exposes "
    "Ethereum network data (ClickHouse, Prometheus, Loki, Dora, ethnode). Use the "
    "provided tools to answer the user's question. Prefer running Python via the "
    "execute_python tool to query data, and use the search tool to discover examples "
    "and schemas before querying. When you have the answer, respond in plain text."
)


class OpenAIAgent:
    """Agent backend that talks to an OpenAI-compatible endpoint plus panda MCP."""

    def __init__(self, settings: EvalSettings) -> None:
        self.settings = settings
        self._tool_calls: list[ToolCallRecord] = []
        self._resources_read: list[str] = []

        base_url = settings.agent_base_url or DEFAULT_OPENAI_BASE_URL
        api_key = os.environ.get(settings.agent_api_key_env)
        if not api_key:
            raise ValueError(
                f"Agent API key not set. Expected environment variable "
                f"'{settings.agent_api_key_env}' for the OpenAI-class agent backend."
            )

        self._client = AsyncOpenAI(
            base_url=base_url,
            api_key=api_key,
            default_headers={
                "HTTP-Referer": "https://github.com/ethpandaops/panda",
                "X-Title": "panda-eval",
            },
        )

    # --- compatibility shims so the shared pytest harness treats both backends alike ---
    @property
    def langfuse(self) -> None:
        return None

    @property
    def current_trace_id(self) -> None:
        return None

    def flush(self) -> None:
        return None

    def _to_openai_tool(self, tool: Any) -> dict[str, Any]:
        return {
            "type": "function",
            "function": {
                "name": MCP_TOOL_PREFIX + tool.name,
                "description": tool.description or "",
                "parameters": tool.inputSchema or {"type": "object", "properties": {}},
            },
        }

    @staticmethod
    def _tool_result_text(result: Any) -> str:
        parts: list[str] = []
        for block in getattr(result, "content", None) or []:
            text = getattr(block, "text", None)
            parts.append(text if text is not None else str(block))
        return "\n".join(parts)

    def _reasoning_body(self) -> dict[str, Any]:
        effort = self.settings.reasoning_effort.lower()
        if effort in ("low", "medium", "high"):
            return {"reasoning": {"effort": effort}}
        return {}

    async def execute(
        self,
        prompt: str,
        session_id: str | None = None,
        test_id: str | None = None,
    ) -> ExecutionResult:
        """Run one task: tool-calling loop against panda MCP until the model answers."""
        self._tool_calls = []
        start_time = time.time()
        result = ExecutionResult(output="", session_id=session_id)

        output_text = ""
        input_tokens = 0
        output_tokens = 0
        cost = 0.0

        try:
            async with sse_client(f"{self.settings.mcp_url}/sse") as (read, write):
                async with ClientSession(read, write) as session:
                    await session.initialize()
                    listed = await session.list_tools()
                    tools = [self._to_openai_tool(t) for t in listed.tools]

                    messages: list[dict[str, Any]] = [
                        {"role": "system", "content": SYSTEM_PROMPT},
                        {"role": "user", "content": prompt},
                    ]

                    for _ in range(self.settings.max_turns):
                        response = await self._client.chat.completions.create(
                            model=self.settings.model,
                            messages=messages,
                            tools=tools,
                            tool_choice="auto",
                            extra_body=self._reasoning_body(),
                        )

                        if response.usage:
                            usage = response.usage.model_dump()
                            input_tokens += usage.get("prompt_tokens") or 0
                            output_tokens += usage.get("completion_tokens") or 0
                            cost += usage.get("cost") or 0.0

                        message = response.choices[0].message

                        if not message.tool_calls:
                            output_text = message.content or ""
                            break

                        messages.append({
                            "role": "assistant",
                            "content": message.content or "",
                            "tool_calls": [tc.model_dump() for tc in message.tool_calls],
                        })

                        for tool_call in message.tool_calls:
                            name = tool_call.function.name
                            try:
                                args = json.loads(tool_call.function.arguments or "{}")
                            except json.JSONDecodeError:
                                args = {}

                            record = ToolCallRecord(name=name, input=args)
                            mcp_name = name.removeprefix(MCP_TOOL_PREFIX)
                            tool_start = time.time()
                            try:
                                tool_result = await session.call_tool(mcp_name, args)
                                content = self._tool_result_text(tool_result)
                                record.result = content
                                record.is_error = bool(getattr(tool_result, "isError", False))
                            except Exception as exc:  # noqa: BLE001 - surfaced to the agent
                                content = f"error calling {mcp_name}: {exc}"
                                record.result = content
                                record.is_error = True

                            record.duration_ms = int((time.time() - tool_start) * 1000)
                            self._tool_calls.append(record)

                            if self.settings.verbose:
                                print(f"  [Tool] {name}({args})")

                            messages.append({
                                "role": "tool",
                                "tool_call_id": tool_call.id,
                                "content": content,
                            })

        except Exception as exc:  # noqa: BLE001 - reported as a failed execution
            result.is_error = True
            result.error_message = str(exc)

        result.output = output_text
        result.tool_calls = self._tool_calls.copy()
        result.resources_read = self._resources_read.copy()
        result.input_tokens = input_tokens
        result.output_tokens = output_tokens
        result.total_cost_usd = cost or None
        result.duration_ms = int((time.time() - start_time) * 1000)
        return result

    async def execute_multi_turn(
        self,
        prompts: list[str],
        test_id: str | None = None,
    ) -> list[ExecutionResult]:
        """Run prompts in sequence (panda session state is managed via the MCP tools)."""
        results: list[ExecutionResult] = []
        session_id: str | None = None
        for prompt in prompts:
            result = await self.execute(prompt, session_id=session_id, test_id=test_id)
            results.append(result)
            if result.session_id:
                session_id = result.session_id
            if result.is_error:
                break
        return results
