"""_host_serve env: the per-serve XDG isolation must not hide the panda CLI config.

The serve env overrides XDG_CONFIG_HOME (opencode isolation), but the panda CLI
resolves its config through XDG_CONFIG_HOME too — so the host's config must be
pinned through PANDA_CONFIG, which the CLI checks first.
"""

from pathlib import Path

import pytest

from agent.opencode_agent import OpenCodeAgent
from config.settings import EvalSettings


@pytest.fixture
def agent(tmp_path, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path / "home"))
    monkeypatch.delenv("XDG_CONFIG_HOME", raising=False)
    monkeypatch.delenv("XDG_DATA_HOME", raising=False)
    monkeypatch.delenv("PANDA_CONFIG", raising=False)
    return OpenCodeAgent(EvalSettings())


def _serve_env(agent: OpenCodeAgent, workdir: Path) -> dict[str, str]:
    _, _, env = agent._host_serve(workdir, port=12345)
    return env


def _write_host_config(monkeypatch) -> Path:
    cfg = Path.home() / ".config" / "panda" / "config.yaml"
    cfg.parent.mkdir(parents=True, exist_ok=True)
    cfg.write_text('server:\n  base_url: "http://localhost:2480"\n')
    return cfg


def test_pins_host_config_through_panda_config(agent, tmp_path, monkeypatch):
    cfg = _write_host_config(monkeypatch)

    env = _serve_env(agent, tmp_path / "work")

    assert env["PANDA_CONFIG"] == str(cfg)
    # The opencode isolation itself must stay intact.
    assert env["XDG_CONFIG_HOME"] == str(tmp_path / "work" / "config")


def test_existing_panda_config_wins(agent, tmp_path, monkeypatch):
    _write_host_config(monkeypatch)
    monkeypatch.setenv("PANDA_CONFIG", "/explicit/scratch-config.yaml")

    env = _serve_env(agent, tmp_path / "work")

    assert env["PANDA_CONFIG"] == "/explicit/scratch-config.yaml"


def test_no_host_config_sets_nothing(agent, tmp_path):
    env = _serve_env(agent, tmp_path / "work")

    assert "PANDA_CONFIG" not in env


def test_respects_outer_xdg_config_home(agent, tmp_path, monkeypatch):
    xdg = tmp_path / "xdg"
    cfg = xdg / "panda" / "config.yaml"
    cfg.parent.mkdir(parents=True)
    cfg.write_text('server:\n  base_url: "http://localhost:2480"\n')
    monkeypatch.setenv("XDG_CONFIG_HOME", str(xdg))

    env = _serve_env(agent, tmp_path / "work")

    assert env["PANDA_CONFIG"] == str(cfg)
