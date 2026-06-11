"""opencode agent backend for ethpandaops-panda evaluation.

Drives the model under test through an ``opencode serve`` instance using the
opencode Python SDK, against panda's MCP tools. opencode runs the agentic
tool-calling loop; this backend spawns/owns the server, sends each question as
a session prompt, and maps the resulting transcript into the harness's
``ExecutionResult`` so grading, cost tracking, and traces stay backend-agnostic.

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


def _host_panda_config() -> Path | None:
    """The panda CLI config the host environment resolves to, or None.

    Mirrors the CLI's own lookup (pkg/configpath): $XDG_CONFIG_HOME/panda/config.yaml
    when XDG_CONFIG_HOME is set, else ~/.config/panda/config.yaml. The $PANDA_CONFIG
    env var needs no mirroring — when set it is inherited by the serve env and the
    CLI checks it first."""
    base = os.environ.get("XDG_CONFIG_HOME", "").strip() or str(Path.home() / ".config")
    candidate = Path(base) / "panda" / "config.yaml"
    return candidate if candidate.is_file() else None


def _free_port() -> int:
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


# A single `opencode serve` is shared across all OpenCodeAgent instances with the
# same config (keyed by the rendered opencode.json), so a pytest run with a
# function-scoped agent fixture pays the server cold-start once, not per test.
_SHARED_SERVERS: dict[str, subprocess.Popen[bytes]] = {}
_SHARED_URLS: dict[str, str] = {}
_SHARED_CONTAINERS: dict[str, str] = {}  # server key -> docker container name (sandbox mode)
_ATEXIT_REGISTERED = False


def _docker_rm(name: str | None) -> None:
    """Force-remove a sandbox container; best-effort (idempotent if already gone)."""
    if not name:
        return
    try:
        subprocess.run(
            ["docker", "rm", "-f", name], capture_output=True, timeout=20, check=False
        )
    except Exception:  # noqa: BLE001 - teardown is best-effort
        pass


def _cleanup_servers() -> None:
    for proc in list(_SHARED_SERVERS.values()):
        if proc.poll() is None:
            proc.terminate()
            try:
                proc.wait(timeout=5)
            except Exception:  # noqa: BLE001
                proc.kill()
    for name in list(_SHARED_CONTAINERS.values()):
        _docker_rm(name)
    _SHARED_SERVERS.clear()
    _SHARED_URLS.clear()
    _SHARED_CONTAINERS.clear()


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
        # Serializes server spawn so concurrent first-runs on one agent don't race to
        # start duplicate `opencode serve` processes.
        self._ensure_lock = asyncio.Lock()

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
            # Headless auto-approve, set HERE so the eval doesn't depend on (or inherit) the
            # user's global ~/.config/opencode permission. Combined with the isolated
            # XDG_CONFIG_HOME below, opencode runs from this config alone.
            "permission": {"*": "allow"},
        }
        if self.route == "mcp":
            mcp_url = self.settings.mcp_url.rstrip("/") + "/mcp"
            cfg["mcp"] = {
                "panda": {"type": "remote", "url": mcp_url, "enabled": True},
            }
        return cfg

    async def _ensure_server(self) -> None:
        # Fast path (lock-free): reuse a live shared server for this exact config.
        if self._client is not None and self._server_key:
            proc = _SHARED_SERVERS.get(self._server_key)
            if proc is not None and proc.poll() is None:
                return
        async with self._ensure_lock:
            await self._ensure_server_locked()

    async def _ensure_server_locked(self) -> None:
        # Re-check under the lock: a concurrent run may have already spawned the server.
        if self._client is not None and self._server_key:
            proc = _SHARED_SERVERS.get(self._server_key)
            if proc is not None and proc.poll() is None:
                return
            self._client = None  # shared server died; respawn below

        if (
            self.provider_id in ("opencode-go", "opencode")
            and not os.environ.get("OPENCODE_GO_API_KEY")
            and not os.environ.get("OPENCODE_API_KEY")
        ):
            # Only the opencode-go provider needs the key; other providers (e.g.
            # openai via ChatGPT OAuth) authenticate from the mounted auth.json.
            raise ValueError(
                "OPENCODE_GO_API_KEY (or OPENCODE_API_KEY) must be set (and exported) "
                f"for the {self.provider_id} provider."
            )

        key = json.dumps(self._opencode_config(), sort_keys=True)
        self._server_key = key

        proc = _SHARED_SERVERS.get(key)
        base = _SHARED_URLS.get(key)
        if proc is None or proc.poll() is not None or not base:
            workdir = Path(tempfile.mkdtemp(prefix="panda-opencode-"))
            (workdir / "opencode.json").write_text(json.dumps(self._opencode_config(), indent=2))
            log_path = workdir / "serve.log"
            port = _free_port()
            container = None
            if self.settings.opencode_sandbox:
                cmd, cwd, env, container = self._docker_serve(workdir, port)
            else:
                cmd, cwd, env = self._host_serve(workdir, port)
            proc = subprocess.Popen(
                cmd, cwd=cwd, env=env, stdout=open(log_path, "wb"), stderr=subprocess.STDOUT
            )
            base = f"http://127.0.0.1:{port}"
            try:
                await self._wait_ready(proc, base, log_path)
            except Exception:
                _docker_rm(container)
                raise
            _SHARED_SERVERS[key] = proc
            _SHARED_URLS[key] = base
            if container:
                _SHARED_CONTAINERS[key] = container
            global _ATEXIT_REGISTERED
            if not _ATEXIT_REGISTERED:
                atexit.register(_cleanup_servers)
                _ATEXIT_REGISTERED = True

        self._proc = proc
        self._base_url = base
        from opencode_ai import AsyncOpencode

        self._client = AsyncOpencode(base_url=base, timeout=float(self.settings.opencode_timeout))

    def _host_serve(
        self, workdir: Path, port: int
    ) -> tuple[list[str], str, dict[str, str]]:
        """Run opencode on the host. Per-serve XDG dirs isolate it from the user's global
        ~/.config/opencode (skills, plugins, providers, permissions) AND give each serve its
        own fresh opencode.db to migrate uncontended (parallel serves otherwise race the
        one-time DB migration and all but one crash). Auth is seeded into the data dir.

        NOTE: host mode still shares the host filesystem — the agent's bash can read the repo
        (and thus the eval cases). Use opencode_sandbox for the isolated, repo-blind run."""
        datadir = workdir / "share"
        confdir = workdir / "config"
        confdir.mkdir(parents=True, exist_ok=True)
        self._seed_auth(datadir)
        env = os.environ.copy()
        env["XDG_DATA_HOME"] = str(datadir)
        env["XDG_CONFIG_HOME"] = str(confdir)
        # The XDG override also redirects the panda CLI's config lookup
        # ($XDG_CONFIG_HOME/panda/config.yaml takes precedence over ~/.config), so
        # `panda` commands run by the agent would see "no config found" even though
        # the host has a config. PANDA_CONFIG is checked before the XDG path — pin
        # the host's config through it so the opencode isolation can't hide it.
        if not env.get("PANDA_CONFIG"):
            host_config = _host_panda_config()
            if host_config is not None:
                env["PANDA_CONFIG"] = str(host_config)
        return (["opencode", "serve", "--port", str(port)], str(workdir), env)

    def _docker_serve(
        self, workdir: Path, port: int
    ) -> tuple[list[str], None, dict[str, str], str]:
        """Run opencode inside a container with NO repo mount — only a cross-compiled linux
        `panda` binary + a panda config + the opencode auth are mounted in, and the panda
        server is reached over host.docker.internal. The subject's bash sees the container's
        filesystem, never the host's, so it cannot read the eval cases.

        Foreground ``docker run`` (not -d) so the Popen tracks liveness/teardown exactly like
        the host serve; ``--rm`` + an explicit name (force-removed on close) handle cleanup."""
        panda_bin = os.environ.get("OPENCODE_SANDBOX_PANDA_BIN")
        if not panda_bin or not Path(panda_bin).exists():
            raise RuntimeError(
                "opencode_sandbox is on but OPENCODE_SANDBOX_PANDA_BIN is unset or missing; "
                "the harness must cross-build a linux panda binary first."
            )
        server_url = os.environ.get(
            "OPENCODE_SANDBOX_SERVER_URL", "http://host.docker.internal:2481"
        )
        image = os.environ.get("OPENCODE_SANDBOX_IMAGE", "panda-opencode-eval:latest")
        # Seed the FULL host opencode auth (a COPY of its content — the host file is never
        # mounted) so every configured provider works in the sandbox: the opencode-go api key
        # AND the openai oauth used by gpt-5.4-mini, etc. Mounted WRITABLE so opencode can
        # refresh an oauth token in-container; the copy is discarded with the workdir.
        src_base = Path(os.environ.get("XDG_DATA_HOME") or (Path.home() / ".local" / "share"))
        src_auth = src_base / "opencode" / "auth.json"
        (workdir / "auth.json").write_text(src_auth.read_text() if src_auth.exists() else "{}")
        (workdir / "panda-config.yaml").write_text(f'server:\n  base_url: "{server_url}"\n')
        # Mount the binary's DIRECTORY (image symlinks /usr/local/bin/panda -> it) so a
        # rebuilt panda is picked up live; the dir holds only `panda`.
        panda_dir = Path(panda_bin).resolve().parent
        name = f"panda-oc-eval-{port}"
        _docker_rm(name)  # clear any stale container on this port
        cmd = [
            "docker", "run", "--rm", "--name", name,
            "-p", f"127.0.0.1:{port}:{port}",
            "--add-host=host.docker.internal:host-gateway",
            "-e", "OPENCODE_GO_API_KEY",  # pass through (value stays out of the arg list)
            "-v", f"{panda_dir}:/opt/pandabin:ro",
            "-v", f"{workdir / 'opencode.json'}:/work/opencode.json:ro",
            "-v", f"{workdir / 'auth.json'}:/root/.local/share/opencode/auth.json",
            "-v", f"{workdir / 'panda-config.yaml'}:/root/.config/panda/config.yaml:ro",
            image,
            "opencode", "serve", "--hostname", "0.0.0.0", "--port", str(port),
        ]
        return (cmd, None, os.environ.copy(), name)

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
    async def _wait_ready(proc: subprocess.Popen[bytes], base: str, log_path: Path) -> None:
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
        container = _SHARED_CONTAINERS.pop(key, None) if key else None
        if key:
            _SHARED_URLS.pop(key, None)
        target = proc or self._proc
        if target is not None and target.poll() is None:
            target.terminate()
            try:
                target.wait(timeout=5)
            except Exception:  # noqa: BLE001
                target.kill()
        _docker_rm(container)  # no-op unless this was a sandboxed serve
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

    @staticmethod
    def _tool_duration_ms(state: dict[str, Any]) -> int:
        """Per-tool wall time from opencode's tool-state ``time: {start, end}`` (ms)."""
        t = state.get("time") or {}
        start, end = t.get("start"), t.get("end")
        if isinstance(start, (int, float)) and isinstance(end, (int, float)) and end >= start:
            return int(end - start)
        return 0


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
        # Clear first: if _ensure_server() raises, current_trace_id must not retain the
        # PREVIOUS run's id (else a caller would attach this failed run's scores there).
        self._current_trace_id = None
        await self._ensure_server()
        start = time.time()
        # Langfuse trace ids are 32 lowercase hex chars (not UUID-dashed).
        self._current_trace_id = uuid.uuid4().hex if self._langfuse else None
        result = ExecutionResult(output="", session_id=session_id)
        # Stamp the identity onto the result NOW so callers don't read it back off the
        # mutable agent property after later awaits (race-safe across runs).
        result.trace_id = self._current_trace_id
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
            except TimeoutError:
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
                        rec.duration_ms = self._tool_duration_ms(st)
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
                        "n_tools": len(result.tool_calls),
                        "n_tool_errors": sum(1 for tc in result.tool_calls if tc.is_error),
                    },
                ) as root_span:
                    # One span per step with the FULL raw input + output — the reasoning
                    # surface is the raw content of each step, not pre-digested fields.
                    for tc in result.tool_calls:
                        with self._langfuse.start_as_current_observation(
                            name=tc.name or "tool",
                            as_type="tool",
                            input=tc.input,
                            output=tc.result,
                            metadata={"is_error": tc.is_error, "duration_ms": tc.duration_ms},
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
