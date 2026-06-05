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
import uuid
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

# A stable per-run session id so every trace from this eval run groups into one
# Langfuse session. In CI that's the GitHub run id; locally a per-process uuid.
_LANGFUSE_SESSION_ID = os.environ.get("GITHUB_RUN_ID") or uuid.uuid4().hex

SYSTEM_PROMPT_MCP = "You are an ethpandaops agent. You have access to panda via its MCP tools."

SYSTEM_PROMPT_CLI = "You are an ethpandaops agent. You have access to the panda CLI."


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

        # Langfuse trace export (optional; gated on langfuse_enabled + keys).
        self._langfuse: Any = None
        self._current_trace_id: str | None = None
        if settings.langfuse_enabled and settings.langfuse_public_key:
            from langfuse import Langfuse

            self._langfuse = Langfuse(
                public_key=settings.langfuse_public_key,
                secret_key=settings.langfuse_secret_key,
                host=settings.langfuse_host,
            )

    @property
    def langfuse(self) -> Any:
        """Return the Langfuse client (or None) for external score recording."""
        return self._langfuse

    @property
    def current_trace_id(self) -> str | None:
        """Return the current Langfuse trace id for external score recording."""
        return self._current_trace_id

    @property
    def langfuse_session_id(self) -> str | None:
        """Return the shared Langfuse session id grouping this run's traces."""
        return _LANGFUSE_SESSION_ID if self._langfuse is not None else None

    def flush(self) -> None:
        """Flush pending Langfuse events so they're sent before the process exits."""
        if self._langfuse is not None:
            self._langfuse.flush()

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
            # Isolate this serve's opencode data dir. opencode runs one server per
            # user, all sharing ~/.local/share/opencode/opencode.db; under parallel
            # pytest-xdist workers, N servers race the DB's first-time migration and
            # all but one crash ("exited before ready: Performing one time database
            # migration"). A per-serve XDG_DATA_HOME gives each its own fresh DB to
            # migrate uncontended; auth is seeded in from the real data dir.
            datadir = workdir / "share"
            self._seed_auth(datadir)
            env = os.environ.copy()
            env["XDG_DATA_HOME"] = str(datadir)
            log_path = workdir / "serve.log"
            port = _free_port()
            proc = subprocess.Popen(
                ["opencode", "serve", "--port", str(port)],
                cwd=str(workdir),
                env=env,
                stdout=open(log_path, "wb"),
                stderr=subprocess.STDOUT,
            )
            base = f"http://127.0.0.1:{port}"
            await self._wait_ready(proc, base, log_path)
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
    def _seed_auth(datadir: Path) -> None:
        """Copy opencode provider auth into an isolated XDG_DATA_HOME so a serve
        spawned with that data dir can authenticate. Source is the real opencode
        data dir, where `opencode auth login` (or CI) writes auth.json."""
        src_base = Path(os.environ.get("XDG_DATA_HOME") or (Path.home() / ".local" / "share"))
        src = src_base / "opencode" / "auth.json"
        dst = datadir / "opencode" / "auth.json"
        dst.parent.mkdir(parents=True, exist_ok=True)
        if src.exists():
            dst.write_bytes(src.read_bytes())
            try:
                dst.chmod(0o600)
            except OSError:
                pass

    @staticmethod
    async def _wait_ready(proc: "subprocess.Popen[bytes]", base: str, log_path: Path) -> None:
        import httpx

        def _tail() -> str:
            try:
                return log_path.read_text(errors="replace")[-2000:].strip() or "(empty serve log)"
            except Exception:  # noqa: BLE001
                return "(no serve log)"

        deadline = time.time() + 45
        async with httpx.AsyncClient() as probe:
            while time.time() < deadline:
                if proc.poll() is not None:
                    raise RuntimeError(
                        f"opencode serve exited (code {proc.returncode}) before ready:\n{_tail()}"
                    )
                try:
                    r = await probe.get(base + "/app", timeout=2)
                    if r.status_code == 200:
                        return
                except Exception:
                    pass
                await asyncio.sleep(0.5)
        raise RuntimeError(f"opencode serve not ready within 45s:\n{_tail()}")

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
        # Langfuse trace ids are 32 lowercase hex chars (not UUID-dashed).
        self._current_trace_id = uuid.uuid4().hex if self._langfuse else None
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

            try:
                await asyncio.wait_for(
                    client.session.chat(
                        id=sid,
                        provider_id=self.provider_id,
                        model_id=self.model_id,
                        parts=[{"type": "text", "text": self._prompt(prompt)}],
                        system=self._system_prompt(),
                    ),
                    timeout=self.settings.opencode_timeout,
                )
            except (asyncio.TimeoutError, TimeoutError):
                raise RuntimeError(
                    f"opencode timed out after {self.settings.opencode_timeout:.0f}s"
                ) from None

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

        if self._langfuse is not None and self._current_trace_id:
            self._record_langfuse_trace(
                trace_id=self._current_trace_id,
                test_id=test_id,
                prompt=self._prompt(prompt),
                session_id=result.session_id,
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
        """Export one execution to Langfuse (root span + tool spans + generation)."""
        if self._langfuse is None:
            return

        # A clickable link back to the CI run (GitHub-only env vars).
        ci_run_url = None
        server, repo, run_id = (
            os.environ.get("GITHUB_SERVER_URL"),
            os.environ.get("GITHUB_REPOSITORY"),
            os.environ.get("GITHUB_RUN_ID"),
        )
        if server and repo and run_id:
            ci_run_url = f"{server}/{repo}/actions/runs/{run_id}"

        # Tracing is best-effort: a Langfuse hiccup must never fail the eval itself.
        try:
            from langfuse import propagate_attributes

            # Group every trace from this eval run under one Langfuse session.
            with propagate_attributes(session_id=_LANGFUSE_SESSION_ID):
                with self._langfuse.start_as_current_observation(
                    trace_context={"trace_id": trace_id},
                    name=test_id or "panda-eval",
                    as_type="span",
                    input={"prompt": prompt},
                    metadata={
                        "model": self.settings.model,
                        "route": self.route,
                        "test_id": test_id,
                        "session_id": session_id,
                        "is_error": result.is_error,
                        "error_message": result.error_message,
                        "num_turns": result.num_turns,
                        "ci_run_url": ci_run_url,
                    },
                ) as root_span:
                    for tc in result.tool_calls:
                        with self._langfuse.start_as_current_observation(
                            name=tc.name or "tool",
                            as_type="tool",
                            input=tc.input,
                            output=tc.result,
                            metadata={"is_error": tc.is_error},
                        ):
                            pass

                    if result.input_tokens or result.output_tokens:
                        with self._langfuse.start_as_current_observation(
                            name=f"{self.provider_id}-completion",
                            as_type="generation",
                            model=self.model_id,
                            usage_details={
                                "input": result.input_tokens,
                                "output": result.output_tokens,
                            },
                            output={"response": result.output},
                        ):
                            pass

                    root_span.update(output={"response": result.output})
                    root_span.set_trace_io(
                        input={"prompt": prompt}, output={"response": result.output}
                    )
            # Force the OTEL batch out now; the eval process is short-lived.
            self._langfuse.flush()
        except Exception as exc:  # noqa: BLE001 - tracing is best-effort
            print(f"  [langfuse] trace export failed: {type(exc).__name__}: {exc}")

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
