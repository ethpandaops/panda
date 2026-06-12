"""Mint-a-case PR flow: append an approved case to a tests/eval/cases file in a
throwaway worktree of ../panda, commit, push, and open a minimal PR.

The diff is just the case block — id, input, tags, rubric — which IS the test,
so nothing extra (answers, traces, reviewer notes) goes in the PR body.
"""

from __future__ import annotations

import re
import subprocess
from pathlib import Path
from typing import Any

import yaml

from studio.fixes import FIXES_DIR, PANDA_REPO, _REPO_GIT_LOCK, _git, now_iso

CASES_DIR_REL = "tests/eval/cases"


def list_cases_files() -> list[str]:
    """The *.yaml case files as they exist on origin/master right now."""
    with _REPO_GIT_LOCK:
        _git(["fetch", "origin", "master"])
        out = _git(["ls-tree", "--name-only", "origin/master", f"{CASES_DIR_REL}/"])
    return sorted(
        Path(p).name for p in out.splitlines() if p.endswith(".yaml")
    )


def _seq_indent(doc: str) -> str:
    """Indentation of the file's top-level case sequence (`- id:` lines)."""
    m = re.search(r"^([ \t]*)- id:", doc, re.M)
    return m.group(1) if m else ""


def case_block(
    case_id: str, q: dict[str, Any], description: str, tags: list[str], rubric: str, indent: str = ""
) -> str:
    i = indent
    rubric_block = "\n".join(f"{i}        " + line for line in rubric.rstrip().splitlines()) or f"{i}        TODO"
    desc = description or q["question"]
    return (
        f"\n{i}# Minted by panda case studio on {now_iso()} "
        f"(question {q['id']}, human-reviewed).\n"
        f"{i}- id: {case_id}\n"
        f"{i}  description: {_yq(desc)}\n"
        f"{i}  input: {_yq(q['question'])}\n"
        f"{i}  network: {q['network']}\n"
        f"{i}  tags: [{', '.join(tags)}]\n"
        f"{i}  assert:\n"
        f"{i}    - type: llm-rubric\n"
        f"{i}      value: >\n"
        f"{rubric_block}\n"
    )


def _yq(s: str) -> str:
    """YAML-safe double-quoted scalar."""
    return '"' + s.replace("\\", "\\\\").replace('"', '\\"') + '"'


def open_case_pr(
    q: dict[str, Any],
    *,
    case_id: str,
    cases_file: str,
    description: str,
    tags: list[str],
    rubric: str,
) -> dict[str, str]:
    allowed = list_cases_files()
    if cases_file not in allowed:
        raise RuntimeError(f"cases file must be one of {allowed}")
    if not re.fullmatch(r"[a-z0-9_]+", case_id):
        raise RuntimeError("case id must be snake_case [a-z0-9_]")
    if not rubric.strip():
        raise RuntimeError("rubric is empty")

    branch = f"case-studio/case-{case_id}"
    worktree = FIXES_DIR.parent / "case-prs" / case_id / "worktree"
    worktree.parent.mkdir(parents=True, exist_ok=True)

    with _REPO_GIT_LOCK:
        _git(["fetch", "origin", "master"])
        if worktree.exists():
            _git(["worktree", "remove", "--force", str(worktree)], check=False)
        _git(["branch", "-D", branch], check=False)
        _git(["worktree", "add", str(worktree), "-b", branch, "origin/master"])
    try:
        target = worktree / CASES_DIR_REL / cases_file
        doc = target.read_text()
        existing = yaml.safe_load(doc) or []
        if any(c.get("id") == case_id for c in existing):
            raise RuntimeError(f"case id '{case_id}' already exists in {cases_file}")

        with target.open("a") as fh:
            fh.write(case_block(case_id, q, description, tags, rubric, _seq_indent(doc)))
        appended = yaml.safe_load(target.read_text())  # must still parse
        if not any(c.get("id") == case_id for c in appended):
            raise RuntimeError("appended case did not round-trip through YAML")

        _git(["add", str(target)], worktree)
        subprocess.run(
            ["git", "commit", "-q", "-m", f"eval: add {case_id} case"],
            cwd=str(worktree), capture_output=True, text=True, timeout=60, check=True,
        )
        _git(["push", "--force-with-lease", "-u", "origin", branch], worktree)

        models = sorted({r["model"] for r in q["runs"]})
        n_ok = sum(1 for r in q["runs"] if r.get("verdict") == "correct")
        body = (
            f"Adds one eval case to `{CASES_DIR_REL}/{cases_file}`:\n\n"
            f"> {q['question']}\n\n"
            f"| | |\n|---|---|\n"
            f"| network | `{q['network']}` |\n"
            f"| route | `{q['route']}` |\n"
            f"| review | {n_ok}/{len(q['runs'])} agent runs approved by a human |\n"
            f"| models | {', '.join(f'`{m}`' for m in models)} |\n\n"
            f"Verify with:\n```\ncd tests/eval && uv run python -m scripts.eval "
            f"--cases {cases_file} --question-id {case_id} --no-variations\n```\n\n"
            f"_Minted from panda case studio._"
        )
        r = subprocess.run(
            [
                "gh", "pr", "create",
                "--repo", "ethpandaops/panda",
                "--base", "master",
                "--head", branch,
                "--title", f"eval: add {case_id} case",
                "--body", body,
            ],
            cwd=str(worktree), capture_output=True, text=True, timeout=120,
        )
        if r.returncode != 0:
            out = (r.stderr or r.stdout) or ""
            existing_pr = re.search(r"https://github\.com/\S+/pull/\d+", out)
            if not existing_pr:
                raise RuntimeError(f"gh pr create failed: {out[-500:]}")
            url = existing_pr.group(0)
        else:
            url = r.stdout.strip().splitlines()[-1]
        return {"pr_url": url, "branch": branch}
    finally:
        _git(["worktree", "remove", "--force", str(worktree)], check=False)
