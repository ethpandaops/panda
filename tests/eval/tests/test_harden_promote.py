"""Champion promotion: cherry-pick the run's commits onto the invoking checkout,
refusing on dirty trees and aborting cleanly on conflicts."""

from __future__ import annotations

import subprocess
from pathlib import Path

import pytest

from scripts.harden import _promote


def _git(cwd: Path, *args: str) -> str:
    out = subprocess.run(
        ["git", "-C", str(cwd), *args], text=True, capture_output=True, check=True
    )
    return out.stdout.strip()


@pytest.fixture()
def repo(tmp_path: Path) -> Path:
    r = tmp_path / "repo"
    r.mkdir()
    _git(r, "init", "-q", "-b", "main")
    _git(r, "config", "user.email", "t@t")
    _git(r, "config", "user.name", "t")
    (r / "a.txt").write_text("base\n")
    _git(r, "add", "."), _git(r, "commit", "-qm", "base")
    return r


def _harden_branch(repo: Path, content: str = "improved\n") -> str:
    _git(repo, "branch", "harden/x")
    wt = repo.parent / "wt"
    _git(repo, "worktree", "add", "-q", str(wt), "harden/x")
    (wt / "b.txt").write_text(content)
    _git(wt, "add", "."), _git(wt, "commit", "-qm", "harden round 1")
    return "harden/x"


def test_promote_lands_champion_commit(repo: Path) -> None:
    branch = _harden_branch(repo)

    assert _promote(str(repo), branch, log=lambda m: None) is True
    assert (repo / "b.txt").read_text() == "improved\n"
    assert "harden round 1" in _git(repo, "log", "--oneline", "-1")


def test_promote_refuses_dirty_checkout(repo: Path) -> None:
    branch = _harden_branch(repo)
    (repo / "a.txt").write_text("local edit\n")

    assert _promote(str(repo), branch, log=lambda m: None) is False
    assert "harden round 1" not in _git(repo, "log", "--oneline", "-2")


def test_promote_aborts_cleanly_on_conflict(repo: Path) -> None:
    branch = _harden_branch(repo)
    # Conflicting commit on main touching the same file the champion adds.
    (repo / "b.txt").write_text("conflicting\n")
    _git(repo, "add", "."), _git(repo, "commit", "-qm", "conflict")

    assert _promote(str(repo), branch, log=lambda m: None) is False
    # No in-progress cherry-pick left behind; tree is clean.
    assert _git(repo, "status", "--porcelain") == ""
    assert "harden round 1" not in _git(repo, "log", "--oneline", "-2")
