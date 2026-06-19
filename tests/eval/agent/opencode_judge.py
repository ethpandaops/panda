"""opencode-backed judge (grader) — runs an llm-rubric grade through an opencode
``session.chat`` instead of a direct OpenAI API call.

The subject already grades-free runs on the user's Codex/ChatGPT subscription: the
``openai`` opencode provider authenticates from opencode's seeded ``auth.json`` (an
OpenAI OAuth credential, ``auth_mode: chatgpt`` — accountId, NO api key). That same
credential is codex-scoped and is rejected by a direct ``api.openai.com`` call
(``403 Missing scopes: api.model.read``), so the default promptfoo ``openai:chat``
grader (which IS a direct API call needing an API key) cannot use it.

This client mirrors the subject's opencode plumbing but for a single, non-agentic
generation: it spawns an isolated ``opencode serve`` with NO MCP and tools disabled,
seeds the host opencode ``auth.json``, and issues one ``session.chat`` against the
configured provider/model. No OpenAI API key is required — auth flows through opencode
exactly like the subject's runs do.

It is the transport behind the ``openai/gpt-5.x`` judge spec; ``promptfoo/judge.py``
is the promptfoo custom-provider shim that calls it.
"""

from __future__ import annotations

import asyncio
import atexit
import json
import os
import socket
import subprocess
import tempfile
import threading
import time
from pathlib import Path
from typing import Any

# A single `opencode serve` is shared across all judge calls with the same config
# (keyed by the rendered opencode.json), so the whole eval pays the cold start once.
_SHARED_SERVERS: dict[str, subprocess.Popen[bytes]] = {}
_SHARED_URLS: dict[str, str] = {}
_SHARED_CLIENTS: dict[str, Any] = {}
_LOCK = threading.Lock()
_ATEXIT_REGISTERED = False


def _free_port() -> int:
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


def _cleanup_servers() -> None:
    for proc in list(_SHARED_SERVERS.values()):
        if proc.poll() is None:
            proc.terminate()
            try:
                proc.wait(timeout=5)
            except Exception:  # noqa: BLE001 - teardown is best-effort
                proc.kill()
    _SHARED_SERVERS.clear()
    _SHARED_URLS.clear()
    _SHARED_CLIENTS.clear()


def _seed_auth(datadir: Path) -> None:
    """Copy the host opencode provider auth into an isolated XDG_DATA_HOME so a serve
    spawned with that data dir can authenticate. This is the exact same auth.json the
    subject seeds — for the ``openai`` provider it holds the ChatGPT OAuth credential."""
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


class OpencodeJudge:
    """One-shot opencode grader for a given ``provider/model`` (e.g. openai/gpt-5.5).

    Spawns/reuses an isolated ``opencode serve`` and grades a rendered rubric prompt with a
    single ``session.chat`` — no tools, no MCP. Auth flows through opencode's ``auth.json``
    (the Codex OAuth credential for the ``openai`` provider), so no API key is needed."""

    def __init__(self, provider_id: str, model_id: str, *, timeout: float = 120.0) -> None:
        self.provider_id = provider_id
        self.model_id = model_id
        self.timeout = timeout
        self._server_key: str | None = None
        self._client: Any = None

    def _opencode_config(self) -> dict[str, Any]:
        # A judge does not call tools: no MCP block, and tools disabled so the model
        # can only emit the rubric verdict text. Isolated permission/config so the grade
        # does not depend on (or inherit) the user's global opencode setup.
        return {
            "$schema": "https://opencode.ai/config.json",
            "model": f"{self.provider_id}/{self.model_id}",
            "permission": {"*": "allow"},
            "tools": {"*": False},
        }

    def _ensure_server(self) -> None:
        global _ATEXIT_REGISTERED
        key = json.dumps(self._opencode_config(), sort_keys=True)
        with _LOCK:
            proc = _SHARED_SERVERS.get(key)
            if proc is not None and proc.poll() is None and key in _SHARED_CLIENTS:
                self._server_key = key
                self._client = _SHARED_CLIENTS[key]
                return

            workdir = Path(tempfile.mkdtemp(prefix="panda-opencode-judge-"))
            (workdir / "opencode.json").write_text(json.dumps(self._opencode_config(), indent=2))
            datadir = workdir / "share"
            confdir = workdir / "config"
            confdir.mkdir(parents=True, exist_ok=True)
            _seed_auth(datadir)
            env = os.environ.copy()
            env["XDG_DATA_HOME"] = str(datadir)
            env["XDG_CONFIG_HOME"] = str(confdir)
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
            self._wait_ready(proc, base, log_path)

            from opencode_ai import Opencode

            client = Opencode(base_url=base, timeout=self.timeout)
            _SHARED_SERVERS[key] = proc
            _SHARED_URLS[key] = base
            _SHARED_CLIENTS[key] = client
            self._server_key = key
            self._client = client
            if not _ATEXIT_REGISTERED:
                atexit.register(_cleanup_servers)
                _ATEXIT_REGISTERED = True

    @staticmethod
    def _wait_ready(proc: subprocess.Popen[bytes], base: str, log_path: Path) -> None:
        import httpx

        def _tail() -> str:
            try:
                return log_path.read_text(errors="replace")[-2000:].strip() or "(empty serve log)"
            except Exception:  # noqa: BLE001
                return "(no serve log)"

        deadline = time.time() + 45
        while time.time() < deadline:
            if proc.poll() is not None:
                raise RuntimeError(
                    f"opencode serve exited (code {proc.returncode}) before ready:\n{_tail()}"
                )
            try:
                r = httpx.get(base + "/app", timeout=2)
                if r.status_code == 200:
                    return
            except Exception:  # noqa: BLE001
                pass
            time.sleep(0.5)
        raise RuntimeError(f"opencode serve not ready within 45s:\n{_tail()}")

    def grade(self, prompt: str) -> str:
        """Run one grading prompt through opencode and return the model's text response."""
        self._ensure_server()
        client = self._client
        sess = client.session.create()
        client.session.chat(
            id=sess.id,
            provider_id=self.provider_id,
            model_id=self.model_id,
            parts=[{"type": "text", "text": prompt}],
        )
        msgs = client.session.messages(id=sess.id)
        text = ""
        for item in msgs:
            d = item.model_dump(warnings=False) if hasattr(item, "model_dump") else item
            info = d.get("info", {}) or {}
            if info.get("role") != "assistant":
                continue
            for p in d.get("parts", []) or []:
                if isinstance(p, dict) and p.get("type") == "text" and p.get("text"):
                    text = p["text"]
        return text


_JUDGES: dict[tuple[str, str], OpencodeJudge] = {}
_JUDGES_LOCK = threading.Lock()


def grade(provider_id: str, model_id: str, prompt: str, *, timeout: float = 120.0) -> str:
    """Module-level entry point: grade ``prompt`` with ``provider_id/model_id`` via opencode,
    reusing a per-(provider, model) judge so the serve cold-start is paid once."""
    key = (provider_id, model_id)
    with _JUDGES_LOCK:
        judge = _JUDGES.get(key)
        if judge is None:
            judge = OpencodeJudge(provider_id, model_id, timeout=timeout)
            _JUDGES[key] = judge
    return judge.grade(prompt)


def grade_async(provider_id: str, model_id: str, prompt: str, *, timeout: float = 120.0) -> str:
    """Async wrapper so a promptfoo provider's ``call_api`` (run under asyncio) doesn't block
    the loop on the synchronous opencode client."""
    return asyncio.run(_grade_in_thread(provider_id, model_id, prompt, timeout))


async def _grade_in_thread(provider_id: str, model_id: str, prompt: str, timeout: float) -> str:
    return await asyncio.to_thread(grade, provider_id, model_id, prompt, timeout=timeout)
