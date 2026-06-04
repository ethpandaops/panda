"""opencode agent backend for ethpandaops-panda evaluation.

Drives the model under test through an ``opencode serve`` instance using the
opencode Python SDK, against panda's MCP tools. opencode runs the agentic
tool-calling loop; this backend spawns/owns the server, sends each question as
a session prompt, and maps the resulting transcript into the harness's
``ExecutionResult`` so the DeepEval metrics, cost tracking, and traces stay
backend-agnostic.

Two routes are supported via ``settings.opencode_route``:
- ``mcp``: opencode is given panda's MCP server (execute_python/search/...).
- ``cli``: opencode is given no MCP, just its shell tool plus the built ``panda``
  binary on PATH; the prompt is prefixed to steer it through the CLI.
"""

from __future__ import annotations

import asyncio
import atexit
import json
import os
import socket
import subprocess
import tempfile
import time
from pathlib import Path
from typing import TYPE_CHECKING, Any

from agent.wrapper import ExecutionResult, ToolCallRecord

if TYPE_CHECKING:
    from config.settings import EvalSettings

# panda's MCP server advertises exactly these tool names; opencode surfaces them
# prefixed with the MCP server's config key (e.g. panda_execute_python). Strip any
# such key prefix back to the bare tool name so cases/metrics match on
# execute_python / search / manage_session regardless of the opencode server key.
_PANDA_TOOLS = ("execute_python", "manage_session", "search")

SYSTEM_PROMPT_MCP = (
    "You are a data analyst for the ethpandaops 'panda' server, which exposes "
    "Ethereum network data (ClickHouse, Prometheus, Loki, Dora, ethnode) through "
    "MCP tools. Use the panda tools to answer the question: search for examples and "
    "schemas first, then run Python via execute_python to query the data. Do not "
    "fabricate numbers — every figure must come from a tool result. When you have "
    "the answer, state it concisely in plain text."
)

SYSTEM_PROMPT_CLI = (
    "You are a data analyst answering questions about Ethereum network data using "
    "the 'panda' command-line tool, which is installed on PATH. Use your shell tool "
    "to run panda commands (e.g. `panda search examples ...`, `panda execute --code "
    "...`, `panda datasources`) to discover and query the data. Do not fabricate "
    "numbers — every figure must come from a panda command's output. When you have "
    "the answer, state it concisely in plain text."
)


def _free_port() -> int:
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


# A single `opencode serve` is shared across all OpenCodeAgent instances with the
# same config (keyed by the rendered opencode.json), so a pytest run with a
# function-scoped agent fixture pays the server cold-start once, not per test.
_SHARED_SERVERS: dict[str, "subprocess.Popen[bytes]"] = {}
_SHARED_URLS: dict[str, str] = {}
_ATEXIT_REGISTERED = False


def _cleanup_servers() -> None:
    for proc in list(_SHARED_SERVERS.values()):
        if proc.poll() is None:
            proc.terminate()
            try:
                proc.wait(timeout=5)
            except Exception:  # noqa: BLE001
                proc.kill()
    _SHARED_SERVERS.clear()
    _SHARED_URLS.clear()


class OpenCodeAgent:
    """Agent backend that drives an opencode server against panda."""

    def __init__(self, settings: EvalSettings) -> None:
        self.settings = settings
        self.route = getattr(settings, "opencode_route", "mcp")

        # settings.model is "<provider>/<model>", e.g. opencode-go/deepseek-v4-flash
        model = settings.model
        if "/" in model:
            self.provider_id, self.model_id = model.split("/", 1)
        else:
            self.provider_id, self.model_id = "opencode-go", model

        self._server_key: str | None = None
        self._proc: subprocess.Popen[bytes] | None = None
        self._base_url: str | None = None
        self._client: Any = None

    # --- compatibility shims so the shared pytest harness treats backends alike ---
    @property
    def langfuse(self) -> None:
        return None

    @property
    def current_trace_id(self) -> None:
        return None

    def flush(self) -> None:
        return None

    # --- server lifecycle ---
    def _opencode_config(self) -> dict[str, Any]:
        cfg: dict[str, Any] = {
            "$schema": "https://opencode.ai/config.json",
            "model": f"{self.provider_id}/{self.model_id}",
        }
        if self.route == "mcp":
            mcp_url = self.settings.mcp_url.rstrip("/") + "/mcp"
            cfg["mcp"] = {
                "panda": {"type": "remote", "url": mcp_url, "enabled": True},
            }
        return cfg

    async def _ensure_server(self) -> None:
        # Reuse a live shared server for this exact config if we already have one.
        if self._client is not None and self._server_key:
            proc = _SHARED_SERVERS.get(self._server_key)
            if proc is not None and proc.poll() is None:
                return
            self._client = None  # shared server died; respawn below

        if not os.environ.get("OPENCODE_GO_API_KEY") and not os.environ.get("OPENCODE_API_KEY"):
            raise ValueError(
                "OPENCODE_GO_API_KEY (or OPENCODE_API_KEY) must be set for the opencode backend."
            )

        key = json.dumps(self._opencode_config(), sort_keys=True)
        self._server_key = key

        proc = _SHARED_SERVERS.get(key)
        base = _SHARED_URLS.get(key)
        if proc is None or proc.poll() is not None or not base:
            workdir = Path(tempfile.mkdtemp(prefix="panda-opencode-"))
            (workdir / "opencode.json").write_text(
                json.dumps(self._opencode_config(), indent=2)
            )
            port = _free_port()
            proc = subprocess.Popen(
                ["opencode", "serve", "--port", str(port)],
                cwd=str(workdir),
                env=os.environ.copy(),
                stdout=None if self.settings.verbose else subprocess.DEVNULL,
                stderr=subprocess.STDOUT if self.settings.verbose else subprocess.DEVNULL,
            )
            base = f"http://127.0.0.1:{port}"
            await self._wait_ready(proc, base)
            _SHARED_SERVERS[key] = proc
            _SHARED_URLS[key] = base
            global _ATEXIT_REGISTERED
            if not _ATEXIT_REGISTERED:
                atexit.register(_cleanup_servers)
                _ATEXIT_REGISTERED = True

        self._proc = proc
        self._base_url = base
        from opencode_ai import AsyncOpencode

        self._client = AsyncOpencode(base_url=base, timeout=float(self.settings.opencode_timeout))

    @staticmethod
    async def _wait_ready(proc: "subprocess.Popen[bytes]", base: str) -> None:
        import httpx

        deadline = time.time() + 60
        async with httpx.AsyncClient() as probe:
            while time.time() < deadline:
                if proc.poll() is not None:
                    raise RuntimeError("opencode serve exited before becoming ready")
                try:
                    r = await probe.get(base + "/app", timeout=2)
                    if r.status_code == 200:
                        return
                except Exception:
                    pass
                await asyncio.sleep(0.5)
        raise RuntimeError("opencode serve did not become ready within 60s")

    def close(self) -> None:
        key = self._server_key
        proc = _SHARED_SERVERS.pop(key, None) if key else None
        if key:
            _SHARED_URLS.pop(key, None)
        target = proc or self._proc
        if target is not None and target.poll() is None:
            target.terminate()
            try:
                target.wait(timeout=5)
            except Exception:  # noqa: BLE001
                target.kill()
        self._proc = None
        self._client = None

    @staticmethod
    def _norm_tool(name: str | None) -> str:
        if not name:
            return ""
        for tool in _PANDA_TOOLS:
            if name == tool or name.endswith("_" + tool):
                return tool
        return name

    @staticmethod
    def _as_dict(obj: Any) -> dict[str, Any]:
        if hasattr(obj, "model_dump"):
            return obj.model_dump(warnings=False)
        return obj if isinstance(obj, dict) else {}

    def _prompt(self, prompt: str) -> str:
        if self.route == "cli":
            return f"Using the panda CLI, {prompt}"
        return prompt

    def _system_prompt(self) -> str:
        return SYSTEM_PROMPT_CLI if self.route == "cli" else SYSTEM_PROMPT_MCP

    async def execute(
        self,
        prompt: str,
        session_id: str | None = None,
        test_id: str | None = None,
    ) -> ExecutionResult:
        """Run one question through opencode; return an ExecutionResult for this turn."""
        await self._ensure_server()
        start = time.time()
        result = ExecutionResult(output="", session_id=session_id)
        client = self._client

        try:
            sid = session_id
            if sid is None:
                sess = await client.session.create()
                sid = sess.id

            # Snapshot existing message ids so we attribute only THIS turn's output
            # (matters when a session is reused across multi-step prompts).
            before = await client.session.messages(id=sid)
            seen = {m.get("id") for m in (self._as_dict(x).get("info", {}) for x in before)}

            await client.session.chat(
                id=sid,
                provider_id=self.provider_id,
                model_id=self.model_id,
                parts=[{"type": "text", "text": self._prompt(prompt)}],
                system=self._system_prompt(),
            )

            after = await client.session.messages(id=sid)

            tool_calls: list[ToolCallRecord] = []
            final_text = ""
            cost = 0.0
            input_tokens = 0
            output_tokens = 0

            for item in after:
                d = self._as_dict(item)
                info = d.get("info", {}) or {}
                if info.get("id") in seen:
                    continue
                if info.get("role") != "assistant":
                    continue
                cost += float(info.get("cost") or 0.0)
                tk = info.get("tokens") or {}
                input_tokens += int(tk.get("input") or 0)
                output_tokens += int(tk.get("output") or 0)
                for p in d.get("parts", []) or []:
                    if not isinstance(p, dict):
                        continue
                    ty = p.get("type")
                    if ty == "tool":
                        st = p.get("state") or {}
                        rec = ToolCallRecord(
                            name=self._norm_tool(p.get("tool")),
                            input=st.get("input") or {},
                        )
                        rec.result = st.get("output")
                        rec.is_error = st.get("status") == "error"
                        tool_calls.append(rec)
                        if self.settings.verbose:
                            print(f"  [Tool] {rec.name}({json.dumps(rec.input)[:120]})")
                    elif ty == "text" and p.get("text"):
                        final_text = p["text"]

            result.session_id = sid
            result.output = final_text
            result.tool_calls = tool_calls
            result.total_cost_usd = cost or None
            result.input_tokens = input_tokens
            result.output_tokens = output_tokens
            result.num_turns = len(tool_calls) + 1
        except Exception as exc:  # noqa: BLE001 - reported as a failed execution
            result.is_error = True
            result.error_message = str(exc)

        result.duration_ms = int((time.time() - start) * 1000)
        return result

    async def execute_multi_turn(
        self,
        prompts: list[str],
        test_id: str | None = None,
    ) -> list[ExecutionResult]:
        """Run prompts in sequence, reusing one opencode session for context continuity."""
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
