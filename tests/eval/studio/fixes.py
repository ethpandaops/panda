"""Fix-dispatch pipeline for panda case studio.

When reviewed agent runs reveal a real panda problem (rather than a mintable
test case), this pipeline has codex produce a fix in a fresh git worktree of
the panda repo, audits the diff adversarially (reusing the harden loop's
CodexAuditor + deterministic leak guards), builds the worktree and re-runs the
original question against a scratch panda server so the human can judge the
fix, and finally — on explicit human approval — pushes a branch and opens a PR.

Every codex pass is a ROUND, committed to the worktree branch, so the attempt
history forms a tree: a human can select any past round, supply fresh hints,
and FORK from that point (the branch tip resets to that round's commit and a
new round grows from it). Rounds record their own cumulative diff, audit
verdict, build result, and verify runs.
"""

from __future__ import annotations

import json
import os
import re
import shutil
import socket
import subprocess
import threading
import uuid
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from harden.auditor import CodexAuditor
from harden.codex import assistant_prose, run_codex
from scripts._panda_env import (
    OPENCODE_IMAGE,
    ScratchServer,
    cross_build_panda_linux,
    ensure_opencode_image,
    write_scratch_config,
)

STUDIO_DIR = Path(__file__).resolve().parent
PANDA_REPO = STUDIO_DIR.parents[2]
DATA_DIR = Path(os.environ.get("STUDIO_DATA_DIR", "~/.panda/studio")).expanduser()
FIXES_DIR = DATA_DIR / "fixes"
FIXES_DIR.mkdir(parents=True, exist_ok=True)

CODEX_MODEL = os.environ.get("STUDIO_CODEX_MODEL", "gpt-5.5")
CODEX_EFFORT = os.environ.get("STUDIO_CODEX_EFFORT", "xhigh")
CODEX_TIMEOUT = float(os.environ.get("STUDIO_CODEX_TIMEOUT", "2400"))
AUTO_AMEND_ROUNDS = 2  # automatic follow-up rounds on audit-block / build failure
VERIFY_TIMEOUT = float(os.environ.get("STUDIO_VERIFY_TIMEOUT", "240"))
MAX_DIFF_STORE = 120000

TERMINAL_FIX = ("pr_open", "failed", "discarded")
ACTIVE_FIX = ("queued", "worktree", "codex", "audit", "amend", "build", "verify", "opening_pr")

FIXES: dict[str, dict[str, Any]] = {}
# Pipelines are isolated (own worktree, dynamic ports, per-fix binaries) and run
# concurrently up to this cap; only two tiny critical sections need real locks.
_PIPELINE_SEM = threading.Semaphore(int(os.environ.get("STUDIO_MAX_FIXES", "10")))
# git worktree add/fetch mutate the SHARED ../panda repo's refs — serialize them.
_REPO_GIT_LOCK = threading.Lock()
# cross_build_panda_linux mutates process-global env; guard mutate+restore+copy.
_ENV_LOCK = threading.Lock()

_RICH = re.compile(r"\[/?(?:dim|bold|red|green|yellow)\]")


def now_iso() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def _atomic_write(path: Path, text: str) -> None:
    tmp = path.with_suffix(path.suffix + ".tmp")
    tmp.write_text(text)
    tmp.replace(path)


def save_fix(f: dict[str, Any]) -> None:
    _atomic_write(FIXES_DIR / f"{f['id']}.json", json.dumps(f, indent=2, default=str))


def _migrate_legacy(f: dict[str, Any]) -> None:
    """Fixes created before the rounds model get one synthesized round carrying
    their stored diff/audit/verify; its commit is created lazily on fork/PR."""
    if "rounds" in f:
        return
    audits = f.get("audits") or []
    f["rounds"] = [
        {
            "idx": 1,
            "parent": 0,
            "kind": "initial",
            "hints": f.get("hints", ""),
            "commit": None,
            "diff": f.get("diff", ""),
            "diff_stat": f.get("diff_stat", ""),
            "audit": audits[-1] if audits else None,
            "guard_problems": [],
            "build_error": None,
            "verify": f.get("verify", []),
            "status": "verified" if f.get("verify") else "failed",
            "codex_summary": f.get("codex_summary", ""),
            "at": f.get("created_at", now_iso()),
        }
    ]
    f["current_round"] = 1
    f.setdefault("base", None)


def load_fixes() -> None:
    for p in sorted(FIXES_DIR.glob("f_*.json")):
        try:
            f = json.loads(p.read_text())
        except Exception:
            continue
        if f.get("status") in ACTIVE_FIX:
            f["status"] = "failed"
            f["error"] = "server restarted mid-pipeline (worktree preserved)"
        for rnd in f.get("rounds", []):
            if rnd.get("status") == "running":
                rnd["status"] = "failed"
        _migrate_legacy(f)
        FIXES[f["id"]] = f
        save_fix(f)


def _git(args: list[str], cwd: Path | str = PANDA_REPO, check: bool = True) -> str:
    r = subprocess.run(
        ["git", *args], cwd=str(cwd), capture_output=True, text=True, timeout=300
    )
    if check and r.returncode != 0:
        raise RuntimeError(f"git {' '.join(args[:3])} failed: {r.stderr.strip()[:500]}")
    return r.stdout


def _free_port() -> int:
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


class FixLogger:
    """Appends pipeline + filtered codex output to the fix's log file."""

    def __init__(self, fid: str) -> None:
        self.path = FIXES_DIR / f"{fid}.log"

    def __call__(self, line: str) -> None:
        clean = _RICH.sub("", str(line)).rstrip()
        with self.path.open("a") as fh:
            fh.write(f"{datetime.now().strftime('%H:%M:%S')} {clean}\n")

    def section(self, title: str) -> None:
        self(f"\n━━━ {title} ━━━")


# ---------------------------------------------------------------------------
# prompts
# ---------------------------------------------------------------------------

_FIX_OBJECTIVE = """\
You are fixing a real failure of the `panda` harness (this repository). An AI agent \
using panda against live data answered the question below incorrectly; a human reviewed \
the runs and confirmed the failure. Find the ROOT CAUSE in the harness — docs, examples, \
guides, dataset knowledge packs, module surfaces, server behavior, CLI ergonomics — and \
make a minimal, general fix.

HARD RULES (an adversarial auditor reviews your diff and WILL reject violations):
- Fix the CLASS of failure, never this single question. Ask: would this change help \
other questions of the same kind, and would it make sense to a maintainer who never saw \
this question?
- NO ANSWER LEAKAGE: never bake this question, its specific answer values, or \
question-specific table/column hints into generic, always-loaded surfaces.
- NO MISPLACEMENT: dataset-specific knowledge belongs in that dataset's knowledge pack \
under datasets/<pack>/; module knowledge in that module; no product behavior in the \
proxy; no presentation in server operations.
- Do not modify tests/eval/** or .github/**.
- Keep the diff minimal and focused. Read CLAUDE.md and follow the repo's standards.
- The build must pass: run `go build ./...` (and `golangci-lint run ./...` if you \
changed Go code) before finishing.
- Do NOT commit. Leave your changes in the working tree.

Finish by printing a short root-cause analysis and a summary of your fix.
The FINAL line of your reply must start with `CHANGED:` followed by ONE sentence
(under 140 chars) describing what you changed — it becomes the round's label.
"""

_AMEND_PREFIX = """\
The working tree contains a previous attempt at this fix. Revise it IN PLACE to address \
the feedback below — do not start over unless necessary, keep the same hard rules \
(generalize; no answer leakage; no tests/eval/** or .github/** edits; build must pass), \
and do not commit. The FINAL line of your reply must start with `CHANGED:` followed by
ONE sentence (under 140 chars) describing what you changed this round.

FEEDBACK:
{feedback}
"""


def _condense_runs(runs: list[dict], *, label: str, cap: int = 4) -> str:
    out = []
    for r in runs[:cap]:
        lines = [f"[{label}] model={r['model']}"]
        answer = " ".join((r.get("answer") or r.get("error") or "(no answer)").split())
        lines.append(f"  final answer: {answer[:500]}")
        for tc in (r.get("tool_calls") or [])[:12]:
            arg = " ".join(str(tc.get("input") or "").split())[:220]
            flag = " [ERROR]" if tc.get("status") == "error" else ""
            lines.append(f"  - {tc.get('name')}{flag}: {arg}")
        if len(r.get("tool_calls") or []) > 12:
            lines.append(f"  ... +{len(r['tool_calls']) - 12} more tool calls")
        out.append("\n".join(lines))
    return "\n\n".join(out) or "(none)"


def build_fix_prompt(q: dict[str, Any], fix: dict[str, Any]) -> str:
    bad = [r for r in q["runs"] if r.get("verdict") == "incorrect"]
    good = [r for r in q["runs"] if r.get("verdict") == "correct"]
    parts = [
        _FIX_OBJECTIVE,
        f"THE QUESTION (asked on network={q['network']} via the panda {q['route']}):\n{q['question']}\n",
        f"WHAT WENT WRONG (human reviewer's assessment):\n{fix['problem'] or '(see failing runs)'}\n",
        f"FAILING RUNS (answer + condensed tool trail):\n{_condense_runs(bad, label='WRONG')}\n",
    ]
    if good:
        parts.append(
            f"CORRECT RUNS for contrast (what a working path looks like):\n"
            f"{_condense_runs(good, label='CORRECT', cap=2)}\n"
        )
    if fix.get("expected"):
        parts.append(f"EXPECTED (per the human reviewer): {fix['expected']}\n")
    if fix.get("hints"):
        parts.append(f"HUMAN HINTS on the likely fix:\n{fix['hints']}\n")
    return "\n".join(parts)


# ---------------------------------------------------------------------------
# deterministic guards
# ---------------------------------------------------------------------------


def _leak_terms(q: dict[str, Any], fix: dict[str, Any]) -> set[str]:
    """Literal values whose appearance in ADDED lines is an instant block: numbers
    (3+ digits) drawn from the human-approved answers / expected value."""
    sources = [fix.get("expected") or ""]
    sources += [r.get("answer") or "" for r in q["runs"] if r.get("verdict") == "correct"]
    terms: set[str] = set()
    for s in sources:
        terms.update(m.group(0) for m in re.finditer(r"\d[\d,]{1,}\.?\d*", s))
    return {t for t in terms if len(t.replace(",", "").replace(".", "")) >= 3}


def deterministic_guards(diff: str, q: dict[str, Any], fix: dict[str, Any]) -> list[str]:
    problems: list[str] = []
    if not diff.strip():
        problems.append("codex produced no changes (empty diff)")
        return problems
    for m in re.finditer(r"^\+\+\+ b/(\S+)", diff, re.M):
        path = m.group(1)
        if path.startswith(("tests/eval/", ".github/")):
            problems.append(f"diff touches protected path: {path}")
    added = [l[1:] for l in diff.splitlines() if l.startswith("+") and not l.startswith("+++")]
    terms = _leak_terms(q, fix)
    for line in added:
        for t in terms:
            if t in line:
                problems.append(
                    f"possible answer leakage: added line contains literal '{t}' "
                    f"from the approved answer: {line.strip()[:120]}"
                )
                break
    return problems


# ---------------------------------------------------------------------------
# rounds
# ---------------------------------------------------------------------------


def _stage(f: dict[str, Any], stage: str, note: str = "") -> None:
    f["status"] = stage
    f.setdefault("stage_history", []).append({"stage": stage, "at": now_iso(), "note": note})
    save_fix(f)


def _commit_round(f: dict[str, Any], worktree: Path, n: int) -> str:
    _git(["add", "-A"], worktree)
    subprocess.run(
        ["git", "commit", "--allow-empty", "-q", "-m", f"case-studio {f['id']} round {n}"],
        cwd=str(worktree), capture_output=True, text=True, timeout=60, check=True,
    )
    return _git(["rev-parse", "HEAD"], worktree).strip()


def _cumulative_diff(f: dict[str, Any], worktree: Path, sha: str) -> str:
    if not f.get("base"):
        # Fixes dispatched before the rounds model have no recorded base.
        f["base"] = _git(["merge-base", "origin/master", sha], worktree).strip()
        save_fix(f)
    return _git(["diff", f"{f['base']}..{sha}"], worktree)


def _ensure_round_commit(f: dict[str, Any], worktree: Path, rnd: dict[str, Any]) -> str:
    """Legacy rounds (pre-rounds-model) carry uncommitted worktree changes."""
    if rnd.get("commit"):
        return rnd["commit"]
    rnd["commit"] = _commit_round(f, worktree, rnd["idx"])
    if not f.get("base"):
        f["base"] = _git(["rev-parse", "origin/master"], worktree).strip()
    save_fix(f)
    return rnd["commit"]


def _current_chain(f: dict[str, Any]) -> set[int]:
    """Round idxs on the path from the current tip back to the root."""
    chain: set[int] = set()
    idx = f.get("current_round") or 0
    by_idx = {r["idx"]: r for r in f.get("rounds", [])}
    while idx and idx in by_idx:
        chain.add(idx)
        idx = by_idx[idx].get("parent") or 0
    return chain


def _run_codex_pass(rnd: dict[str, Any], worktree: Path, prompt: str, log: FixLogger) -> None:
    cmd = [
        "codex", "exec",
        "-m", CODEX_MODEL,
        "-c", f"model_reasoning_effort={CODEX_EFFORT}",
        "-C", str(worktree),
        "--skip-git-repo-check",
        "--dangerously-bypass-approvals-and-sandbox",
        "-",
    ]
    code, raw = run_codex(cmd, prompt, timeout=CODEX_TIMEOUT, log=log, prefix="codex| ")
    if code == -1:
        raise RuntimeError(f"codex timed out after {CODEX_TIMEOUT:.0f}s")
    if code != 0:
        tail = " ".join(raw.split())[-400:]
        raise RuntimeError(f"codex exited {code}: {tail}")
    prose = assistant_prose(raw)
    rnd["codex_summary"] = prose[-4000:]
    rnd["summary"] = _short_summary(prose)


def _short_summary(prose: str) -> str:
    """The round's one-line label: the last `CHANGED:` line codex was told to
    emit, else the first non-empty prose line as a fallback."""
    for line in reversed(prose.splitlines()):
        s = line.strip()
        if s.upper().startswith("CHANGED:"):
            return s[8:].strip()[:200]
    for line in prose.splitlines():
        if line.strip():
            return line.strip()[:200]
    return ""


def _build(worktree: Path, log: FixLogger) -> str | None:
    """Build both binaries (+ lint); returns an error string or None."""
    for args in (["go", "build", "-o", "panda-server", "./cmd/server"],
                 ["go", "build", "-o", "panda", "./cmd/panda"]):
        log(f"build| {' '.join(args)}")
        r = subprocess.run(args, cwd=str(worktree), capture_output=True, text=True, timeout=600)
        if r.returncode != 0:
            return f"`{' '.join(args)}` failed:\n{(r.stderr or r.stdout)[-3000:]}"
    if shutil.which("golangci-lint"):
        log("build| golangci-lint run ./...")
        r = subprocess.run(
            ["golangci-lint", "run", "./..."],
            cwd=str(worktree), capture_output=True, text=True, timeout=900,
        )
        if r.returncode != 0:
            return f"golangci-lint failed:\n{(r.stdout or r.stderr)[-3000:]}"
    return None


def _verify(f: dict[str, Any], q: dict[str, Any], worktree: Path, rnd: dict[str, Any], log: FixLogger) -> None:
    port = _free_port()
    cfg = write_scratch_config(port)
    sandbox = bool(q.get("sandbox", False))
    log(f"verify| scratch server on :{port} from {worktree} (sandbox={'on' if sandbox else 'off'})")
    server = ScratchServer(str(worktree), cfg, port)
    server.start()
    try:
        env = os.environ.copy()
        if sandbox:
            # Containerized verify, like the question runs: cross-build the
            # WORKTREE's panda for linux into a fix-owned dir (restoring the
            # studio's global binary pointer, which cross_build mutates) and
            # aim the container at the scratch server via host.docker.internal.
            ensure_opencode_image()
            bin_dir = FIXES_DIR / f["id"] / "sandbox-bin"
            bin_dir.mkdir(parents=True, exist_ok=True)
            with _ENV_LOCK:
                prior = os.environ.get("OPENCODE_SANDBOX_PANDA_BIN")
                built = cross_build_panda_linux(str(worktree))
                if prior is not None:
                    os.environ["OPENCODE_SANDBOX_PANDA_BIN"] = prior
                shutil.copy2(built, bin_dir / "panda")
            env["OPENCODE_SANDBOX_PANDA_BIN"] = str(bin_dir / "panda")
            env["OPENCODE_SANDBOX_SERVER_URL"] = f"http://host.docker.internal:{port}"
            env["OPENCODE_SANDBOX_IMAGE"] = OPENCODE_IMAGE
            env["MCP_EVAL_OPENCODE_SANDBOX"] = "true"
            runner_url = f"http://host.docker.internal:{port}"
        else:
            env["PANDA_CONFIG"] = str(cfg)
            env["PATH"] = f"{worktree}{os.pathsep}{env.get('PATH', '')}"
            env.pop("MCP_EVAL_OPENCODE_SANDBOX", None)
            runner_url = f"http://localhost:{port}"
        env["MCP_EVAL_MCP_URL"] = runner_url
        import sys

        cmd = [
            sys.executable, str(STUDIO_DIR / "verify_runner.py"),
            "--question", q["question"],
            "--model", f["verify_model"],
            "--route", q["route"],
            "--runs", str(f["verify_runs"]),
            "--timeout", str(VERIFY_TIMEOUT),
            "--mcp-url", runner_url,
        ]
        log(f"verify| {f['verify_runs']}x {f['verify_model']} ({q['route']} route)")
        r = subprocess.run(
            cmd, env=env, capture_output=True, text=True,
            timeout=VERIFY_TIMEOUT * f["verify_runs"] + 300,
        )
        if r.returncode != 0:
            raise RuntimeError(f"verify runner failed: {(r.stderr or r.stdout)[-1500:]}")
        rnd["verify"] = json.loads(r.stdout)
        for i, run in enumerate(rnd["verify"]):
            ans = " ".join((run.get("answer") or run.get("error") or "")[:160].split())
            log(f"verify| run {i + 1}: {ans}")
    finally:
        server.stop()
    save_fix(f)


def _audit_text(audit: dict[str, Any]) -> str:
    lines = [audit.get("summary", "")]
    for x in audit.get("findings", []):
        lines.append(f"- [{x.get('severity')}/{x.get('kind')}] {x.get('file')}: {x.get('issue')}")
    return "\n".join(lines)


def _execute_round(
    f: dict[str, Any],
    q: dict[str, Any],
    worktree: Path,
    log: FixLogger,
    *,
    kind: str,
    parent: int,
    prompt: str,
    hints: str,
) -> dict[str, Any]:
    """One codex pass: edit → commit → guards+audit → build → verify.
    Returns the round record; round['status'] tells how far it got."""
    idx = len(f["rounds"]) + 1
    rnd: dict[str, Any] = {
        "idx": idx, "parent": parent, "kind": kind, "hints": hints,
        "commit": None, "diff": "", "diff_stat": "", "audit": None,
        "guard_problems": [], "build_error": None, "verify": [],
        "codex_summary": "", "status": "running", "at": now_iso(),
    }
    f["rounds"].append(rnd)
    f["current_round"] = idx
    save_fix(f)

    if prompt is None:
        log.section(f"round {idx} — adopting existing worktree changes (no codex pass)")
    else:
        log.section(f"round {idx} — codex ({kind})")
        _stage(f, "codex")
        _run_codex_pass(rnd, worktree, prompt, log)

    rnd["commit"] = _commit_round(f, worktree, idx)
    diff = _cumulative_diff(f, worktree, rnd["commit"])
    rnd["diff"] = diff[:MAX_DIFF_STORE]
    rnd["diff_stat"] = _git(["diff", "--stat", f"{f['base']}..{rnd['commit']}"], worktree)[-2000:]
    save_fix(f)

    log.section(f"round {idx} — guards + audit")
    _stage(f, "audit")
    rnd["guard_problems"] = deterministic_guards(diff, q, f)
    if rnd["guard_problems"]:
        rnd["audit"] = {
            "blocked": True,
            "summary": "deterministic guard violations:\n" + "\n".join(f"- {p}" for p in rnd["guard_problems"]),
            "findings": [],
        }
        rnd["status"] = "audit_blocked"
        log(f"audit| BLOCKED (deterministic): {rnd['guard_problems']}")
        save_fix(f)
        return rnd

    verdict = CodexAuditor(str(worktree), log=log).audit(
        diff, [q["question"], f"Reviewer-described problem: {f['problem']}"]
    )
    rnd["audit"] = {
        "blocked": verdict.blocked,
        "summary": verdict.summary,
        "findings": verdict.findings,
        "unavailable": verdict.unavailable,
    }
    log(f"audit| {verdict.text()}")
    save_fix(f)
    if verdict.unavailable:
        raise RuntimeError(f"auditor unavailable, failing closed: {verdict.summary}")
    if verdict.blocked:
        rnd["status"] = "audit_blocked"
        save_fix(f)
        return rnd

    log.section(f"round {idx} — build")
    _stage(f, "build")
    err = _build(worktree, log)
    if err:
        rnd["build_error"] = err[:3000]
        rnd["status"] = "build_failed"
        log(f"build| FAILED: {err[:300]}")
        save_fix(f)
        return rnd

    log.section(f"round {idx} — verify")
    _stage(f, "verify")
    _verify(f, q, worktree, rnd, log)
    rnd["status"] = "verified"
    save_fix(f)
    return rnd


def _round_feedback(rnd: dict[str, Any]) -> str:
    if rnd["status"] == "audit_blocked":
        return _audit_text(rnd["audit"] or {})
    if rnd["status"] == "build_failed":
        return f"the build/lint failed — fix it:\n{rnd['build_error']}"
    return ""


def _drive_rounds(
    f: dict[str, Any],
    q: dict[str, Any],
    worktree: Path,
    log: FixLogger,
    first: dict[str, Any],
) -> None:
    """Auto-amend loop: blocked/broken rounds get up to AUTO_AMEND_ROUNDS
    follow-ups, each a child of the failed round. Ends in awaiting_review or raises."""
    rnd = first
    autos = 0
    while rnd["status"] != "verified":
        feedback = _round_feedback(rnd)
        if not feedback or autos >= AUTO_AMEND_ROUNDS:
            raise RuntimeError(
                f"round {rnd['idx']} ended {rnd['status']} after {autos} auto-amends; "
                "fork from any round with fresh hints"
            )
        autos += 1
        rnd = _execute_round(
            f, q, worktree, log,
            kind="amend", parent=rnd["idx"],
            prompt=_AMEND_PREFIX.format(feedback=feedback), hints=feedback[:500],
        )
    _stage(f, "awaiting_review", "human judges the verify answers, then opens the PR")
    log.section("awaiting human review")


def run_pipeline(f: dict[str, Any], q: dict[str, Any]) -> None:
    """The whole fix pipeline; runs in a worker thread, one job at a time."""
    log = FixLogger(f["id"])
    with _PIPELINE_SEM:
        try:
            log.section("worktree")
            _stage(f, "worktree")
            worktree = FIXES_DIR / f["id"] / "worktree"
            worktree.parent.mkdir(parents=True, exist_ok=True)
            with _REPO_GIT_LOCK:
                _git(["fetch", "origin", "master"])
                _git(["worktree", "add", str(worktree), "-b", f["branch"], "origin/master"])
            f["worktree"] = str(worktree)
            f["base"] = _git(["rev-parse", "origin/master"], worktree).strip()
            log(f"worktree at {worktree} on {f['branch']} (origin/master @ {f['base'][:10]})")
            save_fix(f)

            first = _execute_round(
                f, q, worktree, log,
                kind="initial", parent=0,
                prompt=build_fix_prompt(q, f), hints=f.get("hints", ""),
            )
            _drive_rounds(f, q, worktree, log, first)
        except Exception as exc:  # noqa: BLE001 - everything surfaces to the UI
            f["error"] = f"{type(exc).__name__}: {exc}"
            _stage(f, "failed", f["error"])
            log(f"FAILED: {f['error']}")


def resume(f: dict[str, Any], q: dict[str, Any]) -> None:
    """Pick up a pipeline that was killed mid-flight (e.g. studio restart): the
    worktree's current state — typically a finished-but-uncommitted codex pass —
    becomes a new round that goes straight through guards/audit/build/verify."""
    log = FixLogger(f["id"])
    with _PIPELINE_SEM:
        try:
            worktree = Path(f.get("worktree") or "")
            if not worktree.exists():
                raise RuntimeError("worktree no longer exists; dispatch a new fix")
            f["error"] = None
            parent = f["rounds"][-1]["idx"] if f.get("rounds") else 0
            _stage(f, "amend", "resuming interrupted pipeline from preserved worktree")
            rnd = _execute_round(
                f, q, worktree, log,
                kind="resume", parent=parent, prompt=None, hints="",
            )
            _drive_rounds(f, q, worktree, log, rnd)
        except Exception as exc:  # noqa: BLE001
            f["error"] = f"{type(exc).__name__}: {exc}"
            _stage(f, "failed", f["error"])
            log(f"RESUME FAILED: {f['error']}")


def fork(f: dict[str, Any], q: dict[str, Any], round_idx: int, hints: str) -> None:
    """Reset the worktree to ROUND_IDX's state and grow a new round from it with
    the human's fresh hints. Prior rounds stay recorded (their commits/diffs/
    verifies are kept in the rounds list)."""
    log = FixLogger(f["id"])
    with _PIPELINE_SEM:
        try:
            worktree = Path(f.get("worktree") or "")
            if not worktree.exists():
                raise RuntimeError("worktree no longer exists; dispatch a new fix")
            by_idx = {r["idx"]: r for r in f["rounds"]}
            target = by_idx.get(round_idx)
            if not target:
                raise RuntimeError(f"no round {round_idx}")
            f["error"] = None
            sha = _ensure_round_commit(f, worktree, target)
            log.section(f"fork from round {round_idx} ({sha[:10]})")
            _stage(f, "amend", f"human fork from round {round_idx}")
            _git(["reset", "--hard", sha], worktree)
            first = _execute_round(
                f, q, worktree, log,
                kind="fork", parent=round_idx,
                prompt=_AMEND_PREFIX.format(feedback=f"the human reviewer says:\n{hints}"),
                hints=hints,
            )
            _drive_rounds(f, q, worktree, log, first)
        except Exception as exc:  # noqa: BLE001
            f["error"] = f"{type(exc).__name__}: {exc}"
            _stage(f, "failed", f["error"])
            log(f"FORK FAILED: {f['error']}")


# ---------------------------------------------------------------------------
# human-gated actions
# ---------------------------------------------------------------------------


def open_pr(f: dict[str, Any], q: dict[str, Any], round_idx: int | None = None) -> str:
    worktree = Path(f["worktree"])
    if not worktree.exists():
        raise RuntimeError("worktree no longer exists")
    by_idx = {r["idx"]: r for r in f["rounds"]}
    rnd = by_idx.get(round_idx or f.get("current_round") or 0)
    if not rnd:
        raise RuntimeError("no round to open a PR from")
    log = FixLogger(f["id"])
    log.section(f"opening PR from round {rnd['idx']}")
    prev_status = f["status"]
    _stage(f, "opening_pr")
    try:
        sha = _ensure_round_commit(f, worktree, rnd)
        title = f["pr_title"] or f"fix: {f['problem'][:60]}"
        # Question + metadata ONLY — no answers, no expected values, no codex
        # analysis: the PR is public history and must not leak grading data.
        models = sorted({r["model"] for r in q["runs"]})
        n_bad = sum(1 for r in q["runs"] if r.get("verdict") == "incorrect")
        body = (
            f"An agent answering the following via panda was reviewed and flagged:\n\n"
            f"> {q['question']}\n\n"
            f"| | |\n|---|---|\n"
            f"| network | `{q['network']}` |\n"
            f"| route | `{q['route']}` |\n"
            f"| agent runs | {len(q['runs'])} ({n_bad} flagged incorrect in review) |\n"
            f"| models | {', '.join(f'`{m}`' for m in models)} |\n"
            f"| verification | {f['verify_runs']}× `{f['verify_model']}` re-run against this branch |\n"
            f"| studio fix | `{f['id']}` · round {rnd['idx']} |\n\n"
            f"_Dispatched from panda case studio; diff audited for answer-leakage/"
            f"misplacement before opening._"
        )
        _git(["push", "--force-with-lease", "-u", "origin", f"{sha}:refs/heads/{f['branch']}"], worktree)
        r = subprocess.run(
            [
                "gh", "pr", "create",
                "--repo", "ethpandaops/panda",
                "--base", "master",
                "--head", f["branch"],
                "--title", title,
                "--body", body,
            ],
            cwd=str(worktree), capture_output=True, text=True, timeout=120,
        )
        if r.returncode != 0:
            out = (r.stderr or r.stdout) or ""
            existing = re.search(r"https://github\.com/\S+/pull/\d+", out)
            if not existing:
                raise RuntimeError(f"gh pr create failed: {out[-500:]}")
            f["pr_url"] = existing.group(0)
        else:
            f["pr_url"] = r.stdout.strip().splitlines()[-1]
        f["pr_round"] = rnd["idx"]
        _stage(f, "pr_open", f["pr_url"])
        log(f"PR: {f['pr_url']}")
        return f["pr_url"]
    except Exception as exc:  # noqa: BLE001
        f["error"] = f"{type(exc).__name__}: {exc}"
        _stage(f, prev_status, f"PR attempt failed: {f['error']}")
        log(f"PR FAILED: {f['error']}")
        raise


def discard(f: dict[str, Any]) -> None:
    worktree = f.get("worktree")
    if worktree and Path(worktree).exists():
        _git(["worktree", "remove", "--force", worktree], check=False)
    _git(["branch", "-D", f["branch"]], check=False)
    _stage(f, "discarded")


def new_fix(q: dict[str, Any], body: dict[str, Any]) -> dict[str, Any]:
    fid = "f_" + uuid.uuid4().hex[:10]
    slug = re.sub(r"[^a-z0-9]+", "-", (body.get("problem") or q["question"]).lower()).strip("-")[:32]
    f: dict[str, Any] = {
        "id": fid,
        "question_id": q["id"],
        "created_at": now_iso(),
        "problem": (body.get("problem") or "").strip(),
        "hints": (body.get("hints") or "").strip(),
        "expected": (body.get("expected") or "").strip(),
        "pr_title": (body.get("pr_title") or "").strip(),
        "verify_runs": max(1, min(int(body.get("verify_runs") or 3), 6)),
        "verify_model": body.get("verify_model")
        or next(
            (r["model"] for r in q["runs"] if r.get("verdict") == "incorrect"),
            q["runs"][0]["model"],
        ),
        "branch": f"case-studio/{fid}-{slug}",
        "status": "queued",
        "stage_history": [],
        "rounds": [],
        "current_round": 0,
        "base": None,
        "worktree": None,
        "pr_url": None,
        "pr_round": None,
        "error": None,
    }
    FIXES[fid] = f
    save_fix(f)
    return f


def fix_summary(f: dict[str, Any]) -> dict[str, Any]:
    return {
        "id": f["id"],
        "question_id": f["question_id"],
        "status": f["status"],
        "branch": f["branch"],
        "pr_url": f.get("pr_url"),
        "error": f.get("error"),
        "created_at": f["created_at"],
        "n_rounds": len(f.get("rounds", [])),
    }


def fix_detail(f: dict[str, Any]) -> dict[str, Any]:
    out = dict(f)
    out["chain"] = sorted(_current_chain(f))
    return out
