"""Panda-specific glue for the harden loop: a local scratch server + conditional apply.

This is the env-specific layer the generic loop knows nothing about. It runs panda from
the candidate source as a LOCAL server on a scratch port, so a proposal can be made live
and measured without touching your real (docker) stack:

  - scratch config = your ~/.config/panda/config.yaml with a few overrides:
      port/base_url -> scratch port; sandbox_url -> host.docker.internal (so the docker
      sandbox containers call back to the host server); sandbox.image -> the locally
      built image; storage/cache -> dirs OUTSIDE the repo (so the loop's git-revert/clean
      never wipes them, and the embedding cache persists -> ~7s warm restarts, offline).
  - apply() rebuilds only what the proposal touched (go build for Go, make docker-sandbox
    for the Python sandbox API) and restarts the server only when the Go binary changed
    (a sandbox image rebuild is picked up live on the next execute, no restart needed).

Datasources come from the hosted proxy in your config, which a local server reaches fine.
"""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import time
import urllib.request
from pathlib import Path

import yaml

from harden.logsetup import get_logger

_log = get_logger("env")

HARDEN_HOME = Path.home() / ".panda" / "harden"  # outside any repo -> survives git clean


def write_scratch_config(port: int, *, source: Path | None = None) -> Path:
    """Render the scratch server config from the user's real config + local overrides."""
    source = source or (Path.home() / ".config" / "panda" / "config.yaml")
    cfg = yaml.safe_load(source.read_text()) or {}

    cache = HARDEN_HOME / "cache"
    storage = HARDEN_HOME / "storage"
    shared = HARDEN_HOME / "shared"
    for d in (cache, storage, shared):
        d.mkdir(parents=True, exist_ok=True)

    cfg.setdefault("server", {})
    cfg["server"]["port"] = port
    cfg["server"]["base_url"] = f"http://localhost:{port}"
    cfg["server"]["sandbox_url"] = f"http://host.docker.internal:{port}"
    sb = cfg.setdefault("sandbox", {})
    sb["image"] = "ethpandaops-panda-sandbox:latest"
    sb["network"] = "ethpandaops-panda-harden"
    sb["host_shared_path"] = str(shared)
    # Eval runs many agents at once (questions x phrasings x subjects, -j concurrent);
    # the default 50-session cap can starve them. The sessions are torn down after
    # every measure (purge_sessions), so a high cap costs nothing when idle.
    sb.setdefault("sessions", {})["max_sessions"] = 150
    cfg["storage"] = {"base_dir": str(storage), "cache_dir": str(cache)}
    cfg.setdefault("observability", {})["metrics_enabled"] = False

    out = HARDEN_HOME / "config.yaml"
    out.write_text(yaml.safe_dump(cfg, sort_keys=False))
    return out


class ScratchServer:
    """A locally-run panda-server (candidate source) on a scratch port."""

    def __init__(
        self, repo_dir: str, config_path: Path, port: int, *, ready_timeout: float = 240.0
    ):
        self.repo_dir = repo_dir
        self.config_path = config_path
        self.port = port
        self.ready_timeout = ready_timeout
        self._proc: subprocess.Popen | None = None
        self._log = HARDEN_HOME / "server.log"

    @property
    def health_url(self) -> str:
        return f"http://localhost:{self.port}/health"

    def stop(self) -> None:
        if self._proc and self._proc.poll() is None:
            purge_sessions(self.port)  # tear down sandbox containers before the server dies
            self._proc.terminate()
            try:
                self._proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                self._proc.kill()
        self._proc = None

    def start(self) -> None:
        self.stop()
        binary = str(Path(self.repo_dir) / "panda-server")
        self._proc = subprocess.Popen(
            [binary, "serve", "--config", str(self.config_path)],
            cwd=self.repo_dir,
            stdout=open(self._log, "ab"),  # append so restarts keep history, not truncate
            stderr=subprocess.STDOUT,
        )
        self._wait_ready()

    def _wait_ready(self) -> None:
        deadline = time.time() + self.ready_timeout
        while time.time() < deadline:
            if self._proc and self._proc.poll() is not None:
                tail = self._log.read_text(errors="replace")[-1500:]
                raise RuntimeError(f"scratch server exited before ready:\n{tail}")
            try:
                with urllib.request.urlopen(self.health_url, timeout=3) as r:  # noqa: S310
                    if r.status == 200:
                        return
            except Exception:  # noqa: BLE001 - not up yet
                pass
            time.sleep(1)
        raise RuntimeError(f"scratch server not ready within {self.ready_timeout:.0f}s")


def purge_sessions(port: int) -> int:
    """Destroy all remaining sandbox sessions on the SCRATCH server, called only as it
    shuts down — its containers would otherwise outlive it as orphans (per-agent
    teardown in the provider handles the during-run case; this is the final sweep of a
    dying server, never a server anyone else uses). Best-effort: a dead/unreachable
    server just means nothing to purge."""
    base = f"http://localhost:{port}/api/v1/sessions"
    try:
        with urllib.request.urlopen(base, timeout=10) as r:  # noqa: S310
            sessions = (json.loads(r.read().decode()) or {}).get("sessions") or []
    except Exception:  # noqa: BLE001 - server gone -> nothing to purge
        return 0
    purged = 0
    for s in sessions:
        sid = s.get("session_id")
        if not sid:
            continue
        req = urllib.request.Request(f"{base}/{sid}", method="DELETE")  # noqa: S310
        try:
            with urllib.request.urlopen(req, timeout=10):  # noqa: S310
                purged += 1
        except Exception:  # noqa: BLE001 - already gone is fine
            continue
    if purged:
        _log.info(f"[cyan]sandbox[/cyan] purged {purged} session(s)")
    return purged


def _sandbox_hash(repo: str) -> str:
    """The repo's own content hash of everything baked into the sandbox image."""
    return subprocess.run(
        ["./scripts/sandbox-hash.sh"], cwd=repo, text=True, capture_output=True, check=True
    ).stdout.strip()


def make_apply(server: ScratchServer, *, sandbox: bool = False):
    """Build the loop's apply(): make the CURRENT working tree live.

    Always rebuilds the Go binaries (incremental, ~instant when unchanged) and restarts
    the server (~7s warm), so what's deployed always matches the tree — including after a
    git revert, which a "diff vs HEAD" check would miss. The sandbox image is rebuilt only
    when its content hash changes (the repo's sandbox-hash.sh), since that build is the
    only slow step.

    When ``sandbox`` (the subject runs in a container), each rebuild also re-cross-compiles
    the candidate linux ``panda`` the container mounts, so the sandboxed agent always uses
    the current CLI.
    """
    repo = server.repo_dir
    state = {"sandbox_hash": None}

    def apply() -> None:
        _log.info("[cyan]build[/cyan] go build panda-server")
        _run(["go", "build", "-o", "panda-server", "./cmd/server"], repo)
        _log.info("[cyan]build[/cyan] go build panda (CLI)")
        _run(["go", "build", "-o", "panda", "./cmd/panda"], repo)
        _log.info("[cyan]lint[/cyan] golangci-lint run")
        _lint(repo)
        if sandbox:
            _log.info("[cyan]build[/cyan] cross-compiling linux panda (sandbox)")
            cross_build_panda_linux(repo)
        h = _sandbox_hash(repo)
        if h != state["sandbox_hash"]:
            _log.info("[cyan]build[/cyan] make docker-sandbox (slow on first build)")
            _run(["make", "docker-sandbox"], repo)
            state["sandbox_hash"] = h
        _log.info(f"[cyan]server[/cyan] starting on :{server.port} (waiting for /health)")
        server.start()
        _log.info(f"[green]server[/green] ready on :{server.port}")

    return apply


# --- sandboxed subject (opencode in a container, no repo access) ---

OPENCODE_IMAGE = "panda-opencode-eval:latest"
_SANDBOX_BIN = HARDEN_HOME / "sandbox-bin"  # holds only `panda` (mounted as a dir)


def _docker_arch() -> str:
    """The docker server's arch — docker's names (arm64/amd64) match GOARCH."""
    out = subprocess.run(
        ["docker", "version", "--format", "{{.Server.Arch}}"], text=True, capture_output=True
    )
    return (out.stdout or "").strip() or "arm64"


def cross_build_panda_linux(repo: str) -> Path:
    """Cross-compile the candidate panda CLI for the docker (linux) arch, statically (no
    cgo). Output lives OUTSIDE the repo so git-clean never wipes it; the container mounts
    its directory. Returns the binary path (``OPENCODE_SANDBOX_PANDA_BIN``)."""
    _SANDBOX_BIN.mkdir(parents=True, exist_ok=True)
    out = _SANDBOX_BIN / "panda"
    env = {**os.environ, "GOOS": "linux", "GOARCH": _docker_arch(), "CGO_ENABLED": "0"}
    proc = subprocess.run(
        ["go", "build", "-o", str(out), "./cmd/panda"],
        cwd=repo, env=env, text=True, capture_output=True,
    )
    if proc.returncode != 0:
        raise RuntimeError(
            f"cross-build linux panda failed ({proc.returncode}):\n{(proc.stderr or '')[-1200:]}"
        )
    os.environ["OPENCODE_SANDBOX_PANDA_BIN"] = str(out)
    return out


def ensure_opencode_image(image: str = OPENCODE_IMAGE) -> None:
    """Build the sandbox opencode image if it isn't already present locally."""
    if subprocess.run(["docker", "image", "inspect", image], capture_output=True).returncode == 0:
        _log.info(f"[cyan]sandbox[/cyan] image {image} present")
        return
    _log.info(f"[cyan]sandbox[/cyan] building image {image} (slow, one-time)")
    eval_dir = str(Path(__file__).resolve().parents[1])
    _run(["docker", "build", "-f", "sandbox/opencode.Dockerfile", "-t", image, "sandbox/"], eval_dir)


def prepare_opencode_sandbox(repo_dir: str, port: int, *, image: str = OPENCODE_IMAGE) -> None:
    """Enable the sandboxed subject: ensure the image, cross-build the candidate linux
    panda, and set the env the agent reads (server URL via host.docker.internal + the
    MCP_EVAL_OPENCODE_SANDBOX flag). The harden loop re-runs the cross-build each round via
    ``make_apply(sandbox=True)``."""
    ensure_opencode_image(image)
    os.environ["OPENCODE_SANDBOX_IMAGE"] = image
    os.environ["OPENCODE_SANDBOX_SERVER_URL"] = f"http://host.docker.internal:{port}"
    os.environ["MCP_EVAL_OPENCODE_SANDBOX"] = "true"
    cross_build_panda_linux(repo_dir)


def _lint(repo: str) -> None:
    """Lint is part of apply(): a lint-dirty candidate is rejected exactly like a broken
    build. Without this, a champion can ship dead code or vet failures straight onto the
    invoking branch via auto-promote (observed: an unused test fake survived to commit).
    Findings go on stdout, which _run discards — capture both streams here."""
    proc = subprocess.run(
        ["golangci-lint", "run", "./..."], cwd=repo, text=True, capture_output=True
    )
    if proc.returncode != 0:
        findings = ((proc.stdout or "") + (proc.stderr or ""))[-1200:]
        raise RuntimeError(f"golangci-lint failed ({proc.returncode}):\n{findings}")


def _run(cmd: list[str], cwd: str) -> None:
    proc = subprocess.run(cmd, cwd=cwd, text=True, capture_output=True)
    if proc.returncode != 0:
        raise RuntimeError(
            f"{' '.join(cmd)} failed ({proc.returncode}):\n{(proc.stderr or '')[-1200:]}"
        )


def point_cli_at_scratch(repo_dir: str, config_path: Path) -> None:
    """Make the opencode CLI route hit the scratch server: the freshly-built `panda`
    binary on PATH + PANDA_CONFIG pointing at the scratch config. Must be called before
    any subject spawns its opencode server (which inherits this process's env)."""
    os.environ["PANDA_CONFIG"] = str(config_path)
    if shutil.which("panda") != str(Path(repo_dir) / "panda"):
        os.environ["PATH"] = f"{repo_dir}{os.pathsep}{os.environ.get('PATH', '')}"
