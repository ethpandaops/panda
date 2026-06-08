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

import os
import shutil
import subprocess
import time
import urllib.request
from pathlib import Path

import yaml

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
            stdout=open(self._log, "wb"),
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


def _sandbox_hash(repo: str) -> str:
    """The repo's own content hash of everything baked into the sandbox image."""
    return subprocess.run(
        ["./scripts/sandbox-hash.sh"], cwd=repo, text=True, capture_output=True, check=True
    ).stdout.strip()


def make_apply(server: ScratchServer):
    """Build the loop's apply(): make the CURRENT working tree live.

    Always rebuilds the Go binaries (incremental, ~instant when unchanged) and restarts
    the server (~7s warm), so what's deployed always matches the tree — including after a
    git revert, which a "diff vs HEAD" check would miss. The sandbox image is rebuilt only
    when its content hash changes (the repo's sandbox-hash.sh), since that build is the
    only slow step.
    """
    repo = server.repo_dir
    state = {"sandbox_hash": None}

    def apply() -> None:
        _run(["go", "build", "-o", "panda-server", "./cmd/server"], repo)
        _run(["go", "build", "-o", "panda", "./cmd/panda"], repo)
        h = _sandbox_hash(repo)
        if h != state["sandbox_hash"]:
            _run(["make", "docker-sandbox"], repo)
            state["sandbox_hash"] = h
        server.start()

    return apply


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
