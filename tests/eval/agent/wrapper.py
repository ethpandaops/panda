"""Claude Agent SDK wrapper for ethpandaops-mcp evaluation with Langfuse tracing."""

from __future__ import annotations

import time
import uuid
from dataclasses import dataclass, field
from typing import TYPE_CHECKING, Any

from claude_agent_sdk import (
    AssistantMessage,
    ClaudeAgentOptions,
    ClaudeSDKClient,
    HookMatcher,
    ResultMessage,
    TextBlock,
    ToolResultBlock,
    ToolUseBlock,
)
from deepeval.tracing import observe

if TYPE_CHECKING:
    from langfuse import Langfuse

    from config.settings import EvalSettings


# Tools to disallow when restrict_to_mcp_tools is enabled
# This prevents agents from exploring the codebase instead of using MCP tools
DISALLOWED_TOOLS = [
    "Bash",
    "Glob",
    "Grep",
    "Read",
    "Write",
    "Edit",
    "LS",
    "MCPSearch",
    "Task",
    "WebFetch",
    "WebSearch",
]


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


class MCPAgent:
    """Wrapper around Claude Agent SDK for ethpandaops-mcp evaluation."""

    def __init__(self, settings: EvalSettings) -> None:
        self.settings = settings
        self._tool_calls: list[ToolCallRecord] = []
        self._resources_read: list[str] = []
        self._current_tool_start: float = 0

        # Langfuse client for trace visualization (optional)
        self._langfuse: Langfuse | None = None
        self._current_trace_id: str | None = None

        if settings.langfuse_enabled and settings.langfuse_public_key:
            from langfuse import Langfuse

            self._langfuse = Langfuse(
                public_key=settings.langfuse_public_key,
                secret_key=settings.langfuse_secret_key,
                host=settings.langfuse_host,
            )

    @property
    def langfuse(self) -> Langfuse | None:
        """Return Langfuse client for external score recording."""
        return self._langfuse

    @property
    def current_trace_id(self) -> str | None:
        """Return current trace ID for external score recording."""
        return self._current_trace_id

    def flush(self) -> None:
        """Flush pending Langfuse events to ensure they're sent."""
        if self._langfuse:
            self._langfuse.flush()

    async def _trace_pre_tool(
        self,
        input_data: dict[str, Any],
        tool_use_id: str | None,
        context: Any,
    ) -> dict[str, Any]:
        """Hook to record tool calls before execution."""
        tool_name = input_data.get("tool_name", "")
        tool_input = input_data.get("tool_input", {})

        self._current_tool_start = time.time()

        record = ToolCallRecord(
            name=tool_name,
            input=tool_input,
        )
        self._tool_calls.append(record)

        # Track resource reads
        if "ReadResource" in tool_name or "read" in tool_name.lower():
            uri = tool_input.get("uri", "")
            if uri:
                self._resources_read.append(uri)

        if self.settings.verbose:
            print(f"  [Tool] {tool_name}")
            if self.settings.log_tool_calls:
                print(f"         Input: {tool_input}")

        return {}

    async def _trace_post_tool(
        self,
        input_data: dict[str, Any],
        tool_use_id: str | None,
        context: Any,
    ) -> dict[str, Any]:
        """Hook to record tool results after execution."""
        if self._tool_calls:
            duration = int((time.time() - self._current_tool_start) * 1000)

            # Debug: log all keys in input_data to understand SDK structure
            if self.settings.verbose and self.settings.log_tool_calls:
                print(f"         [DEBUG] PostToolUse keys: {list(input_data.keys())}")

            # Extract result from various possible fields
            # The Claude Agent SDK may put the result in different places
            result = (
                input_data.get("tool_response")
                or input_data.get("result")
                or input_data.get("content")
                or input_data.get("output")
            )

            # Check if this is an error response
            is_error = input_data.get("is_error", False) or input_data.get(
                "isError", False
            )

            # If result is still None, check if there's error content
            if result is None and is_error:
                result = input_data.get("error") or input_data.get("error_message")

            # If we still have no result, capture the full input_data for debugging
            if result is None and input_data:
                # Filter out None values and capture what we have
                non_null_data = {k: v for k, v in input_data.items() if v is not None}
                if non_null_data:
                    result = non_null_data

            self._tool_calls[-1].result = result
            self._tool_calls[-1].duration_ms = duration
            self._tool_calls[-1].is_error = is_error

            if self.settings.verbose and self.settings.log_tool_calls:
                # Truncate long results
                result_str = str(result) if result else "(no result)"
                if len(result_str) > 500:
                    result_str = result_str[:500] + "..."
                error_marker = " [ERROR]" if is_error else ""
                print(f"         Result{error_marker}: {result_str}")
                print(f"         Duration: {duration}ms")

        return {}

    @observe(type="agent")
    async def execute(
        self,
        prompt: str,
        session_id: str | None = None,
        test_id: str | None = None,
    ) -> ExecutionResult:
        """Execute a prompt and return structured result with metrics.

        Args:
            prompt: The prompt to execute
            session_id: Optional session ID for multi-turn conversations
            test_id: Optional test ID for Langfuse trace naming
        """
        self._tool_calls = []
        self._resources_read = []
        start_time = time.time()

        # Generate trace ID for Langfuse (32 lowercase hex chars, not UUID format)
        self._current_trace_id = uuid.uuid4().hex if self._langfuse else None

        # Build options with optional restrictions
        # When restrict_to_mcp_tools is enabled, we disallow filesystem/codebase tools
        # The agent will get context from the mcp://getting-started resource and tool descriptions
        disallowed = DISALLOWED_TOOLS if self.settings.restrict_to_mcp_tools else []

        options = ClaudeAgentOptions(
            model=self.settings.model,
            permission_mode=self.settings.permission_mode,
            max_turns=self.settings.max_turns,
            disallowed_tools=disallowed,
            mcp_servers={
                "ethpandaops": {
                    "type": "sse",
                    "url": f"{self.settings.mcp_url}/sse",
                }
            },
            allowed_tools=[
                "mcp__ethpandaops__execute_python",
                "mcp__ethpandaops__search",
            ],
            hooks={
                "PreToolUse": [HookMatcher(hooks=[self._trace_pre_tool])],
                "PostToolUse": [HookMatcher(hooks=[self._trace_post_tool])],
            },
        )

        output_parts: list[str] = []
        result = ExecutionResult(output="", session_id=session_id)

        try:
            async with ClaudeSDKClient(options=options) as client:
                # Include session_id in prompt if continuing session
                full_prompt = prompt
                if session_id:
                    full_prompt = f"[Session: {session_id}] {prompt}"

                await client.query(full_prompt)

                async for message in client.receive_response():
                    if isinstance(message, AssistantMessage):
                        for block in message.content:
                            if isinstance(block, TextBlock):
                                output_parts.append(block.text)
                            elif isinstance(block, ToolUseBlock):
                                # Tool use is tracked via hooks
                                pass
                            elif isinstance(block, ToolResultBlock):
                                # Check for session_id in tool results
                                if hasattr(block, "content"):
                                    content_str = str(block.content)
                                    if "session_id" in content_str:
                                        # Try to extract session_id
                                        import re

                                        match = re.search(
                                            r'"session_id":\s*"([^"]+)"', content_str
                                        )
                                        if match:
                                            result.session_id = match.group(1)

                    elif isinstance(message, ResultMessage):
                        result.total_cost_usd = message.total_cost_usd
                        result.duration_ms = message.duration_ms
                        result.num_turns = message.num_turns
                        result.is_error = message.is_error

                        # Extract token usage
                        if message.usage:
                            result.input_tokens = message.usage.get("input_tokens", 0)
                            result.output_tokens = message.usage.get("output_tokens", 0)

                        # Get session_id from result if available
                        if message.session_id:
                            result.session_id = message.session_id

                        break

        except Exception as e:
            result.is_error = True
            result.error_message = str(e)
            result.duration_ms = int((time.time() - start_time) * 1000)

        result.output = "\n".join(output_parts)
        result.tool_calls = self._tool_calls.copy()
        result.resources_read = self._resources_read.copy()

        # Record trace to Langfuse after execution completes
        if self._langfuse and self._current_trace_id:
            self._record_langfuse_trace(
                trace_id=self._current_trace_id,
                test_id=test_id,
                prompt=prompt,
                session_id=session_id,
                result=result,
            )

        return result

    def _record_langfuse_trace(
        self,
        trace_id: str,
        test_id: str | None,
        prompt: str,
        session_id: str | None,
        result: ExecutionResult,
    ) -> None:
        """Record execution trace to Langfuse using SDK v3 API."""
        if not self._langfuse:
            return

        trace_context = {"trace_id": trace_id}

        # Create the root span for this execution
        with self._langfuse.start_as_current_observation(
            trace_context=trace_context,
            name=test_id or "mcp-eval",
            as_type="span",
            input={"prompt": prompt},
            metadata={
                "model": self.settings.model,
                "test_id": test_id,
            },
        ) as root_span:
            # Update trace-level attributes (session_id, etc.)
            root_span.update_trace(
                session_id=session_id,
                metadata={
                    "model": self.settings.model,
                    "is_error": result.is_error,
                    "error_message": result.error_message,
                    "num_turns": result.num_turns,
                    "resources_read": result.resources_read,
                },
            )

            # Add each tool call as a nested span
            for tool_call in self._tool_calls:
                with self._langfuse.start_as_current_observation(
                    name=tool_call.name,
                    as_type="tool",
                    input=tool_call.input,
                    output=tool_call.result,
                    metadata={"duration_ms": tool_call.duration_ms},
                ):
                    pass  # Tool call spans are auto-closed

            # Record LLM generation for cost tracking
            if result.input_tokens or result.output_tokens:
                with self._langfuse.start_as_current_observation(
                    name="claude-completion",
                    as_type="generation",
                    model=self.settings.model,
                    usage_details={
                        "input": result.input_tokens,
                        "output": result.output_tokens,
                    },
                    output={"response": result.output},
                ):
                    pass  # Generation span is auto-closed

            # Update root span with final output
            root_span.update(output={"response": result.output})

    async def execute_multi_turn(
        self,
        prompts: list[str],
        test_id: str | None = None,
    ) -> list[ExecutionResult]:
        """Execute multiple prompts in sequence, preserving session.

        Args:
            prompts: List of prompts to execute in sequence
            test_id: Optional test ID for Langfuse trace naming
        """
        results: list[ExecutionResult] = []
        session_id: str | None = None

        for i, prompt in enumerate(prompts):
            # Use test_id with turn number for multi-turn traces
            turn_test_id = f"{test_id}-turn-{i + 1}" if test_id else None
            result = await self.execute(prompt, session_id=session_id, test_id=turn_test_id)
            results.append(result)

            # Capture session_id for subsequent turns
            if result.session_id:
                session_id = result.session_id

            # Stop if we hit an error
            if result.is_error:
                break

        return results
