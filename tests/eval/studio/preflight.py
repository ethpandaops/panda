"""Environment preflight for panda case studio — fail early, with the fix.

Hard requirements get a ✗ and abort startup; degraded-but-usable ones warn.
"""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
import urllib.request
from pathlib import Path

PANDA_SERVER_URL = os.environ.get("STUDIO_PANDA_SERVER", "http://localhost:2480")


def _run(cmd: list[str], timeout: int = 20) -> tuple[int, str]:
    try:
        r = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)
        return r.returncode, (r.stdout + r.stderr).strip()
    except FileNotFoundError:
        return 127, "not found"
    except Exception as exc:  # noqa: BLE001
        return 1, str(exc)


def _opencode_key() -> bool:
    if os.environ.get("OPENCODE_GO_API_KEY") or os.environ.get("OPENCODE_API_KEY"):
        return True
    base = Path(os.environ.get("XDG_DATA_HOME") or (Path.home() / ".local" / "share"))
    try:
        entry = json.loads((base / "opencode" / "auth.json").read_text()).get("opencode-go") or {}
        return bool(entry.get("key"))
    except Exception:  # noqa: BLE001
        return False


def run_preflight() -> None:
    failures: list[str] = []
    warnings: list[str] = []

    def hard(ok: bool, name: str, fix: str) -> None:
        print(f"  {'✓' if ok else '✗'} {name}")
        if not ok:
            failures.append(f"{name}\n      fix: {fix}")

    def soft(ok: bool, name: str, note: str) -> None:
        print(f"  {'✓' if ok else '!'} {name}")
        if not ok:
            warnings.append(f"{name} — {note}")

    print("panda case studio preflight:")

    # agent runner: opencode + a key for the opencode-go provider
    hard(shutil.which("opencode") is not None, "opencode CLI on PATH",
         "install opencode (https://opencode.ai), then `opencode auth login`")
    hard(_opencode_key(), "opencode-go provider key",
         "run `opencode auth login` and add the opencode-go key, or export OPENCODE_GO_API_KEY")

    # fix pipeline: codex + gh
    hard(shutil.which("codex") is not None, "codex CLI on PATH",
         "npm i -g @openai/codex (the fix pipeline drives `codex exec`)")
    if shutil.which("codex"):
        rc, _ = _run(["codex", "login", "status"])
        hard(rc == 0, "codex authenticated", "run `codex login`")
    rc, _ = _run(["gh", "auth", "status"])
    hard(rc == 0, "gh authenticated (PRs)", "run `gh auth login`")

    # sandboxed runs: docker daemon
    rc, _ = _run(["docker", "info", "--format", "{{.ServerVersion}}"], timeout=15)
    hard(rc == 0, "docker daemon running (sandboxed agents)", "start Docker Desktop / colima")

    # the panda server agents talk to
    try:
        with urllib.request.urlopen(f"{PANDA_SERVER_URL}/health", timeout=4) as resp:
            server_ok = resp.status == 200
    except Exception:  # noqa: BLE001
        server_ok = False
    hard(server_ok, f"panda-server at {PANDA_SERVER_URL}",
         "`docker compose up -d` in the repo root (or set STUDIO_PANDA_SERVER)")

    # fix-pipeline builds
    hard(shutil.which("go") is not None, "go toolchain (fix builds)", "install go")
    soft(shutil.which("golangci-lint") is not None, "golangci-lint",
         "fix builds skip the lint gate without it")
    rc, _ = _run(["docker", "image", "inspect", "panda-opencode-eval:latest"], timeout=15)
    soft(rc == 0, "panda-opencode-eval image",
         "first sandboxed run builds it (slow, one-time)")

    if warnings:
        print("\n  warnings:")
        for w in warnings:
            print(f"    ! {w}")
    if failures:
        print("\npreflight FAILED:", file=sys.stderr)
        for f in failures:
            print(f"  ✗ {f}", file=sys.stderr)
        sys.exit(1)
    print("  preflight OK\n")
