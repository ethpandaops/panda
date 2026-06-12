"""Run the harden optimization loop against the panda harness.

    uv run python -m scripts.harden --tags smoke --rounds 3 --k 3 --sandbox

By default it runs TWO agent subjects — opencode-go/deepseek-v4-flash + openai/gpt-5.4-mini
(over the CLI route) — so an accepted change has to help both, not overfit to one. Override
with one or more --subject provider/model:route.

The loop measures the current harness, lets Codex (GPT-5.5 @ xhigh) edit panda from the
RAW agent traces (eval cases hidden while it runs), prescreens cheaply, re-measures, and
keeps a small Pareto pool of candidate states as mutation parents. A state is only
COMMITTED (once, at the end) if it never regresses correctness and is permutation-test
confidently better on the gate questions. Run it on a throwaway worktree/branch with a
clean tree.

It builds panda from the candidate source and runs it as a LOCAL scratch server (default
:2481), derived from your ~/.config/panda/config.yaml (hosted proxy + datasources work as
is; sandbox callbacks go via host.docker.internal). Embeddings cache under ~/.panda/harden
so restarts are ~7s and offline. Go edits rebuild+restart the server; sandbox-API edits
rebuild the image and are picked up live. Your real stack on :2480 is untouched.
"""

from __future__ import annotations

import argparse
import asyncio
import os
import subprocess
import sys
import time
from pathlib import Path

from cases.loader import load_test_cases
from config.settings import DEFAULT_EVALUATOR_MODEL, DEFAULT_SUBJECTS, grader_for
from harden.auditor import CodexAuditor
from harden.journal import DEFAULT_NAME as JOURNAL_NAME
from harden.journal import Journal
from harden.loop import optimize
from harden.proposer import CodexProposer
from harness.logsetup import setup_logging
from harness.runner import Question
from scripts._panda_env import (
    HARDEN_HOME,
    ScratchServer,
    make_apply,
    point_cli_at_scratch,
    prepare_opencode_sandbox,
    write_scratch_config,
)


def _repo_root() -> str:
    return subprocess.run(
        ["git", "rev-parse", "--show-toplevel"], text=True, capture_output=True, check=True
    ).stdout.strip()


def _auto_worktree(repo_dir: str, ts: str, log) -> tuple[str, str]:
    """A dedicated worktree + branch (harden/<ts>) from the checkout's HEAD for this run.
    Removed at the end if nothing was committed; kept (with instructions) if it was."""
    branch = f"harden/{ts}"
    path = HARDEN_HOME / "worktrees" / ts
    path.parent.mkdir(parents=True, exist_ok=True)
    dirty = subprocess.run(
        ["git", "-C", repo_dir, "status", "--porcelain"], text=True, capture_output=True
    ).stdout.strip()
    if dirty:
        log(
            "[yellow]note[/yellow]: your checkout has uncommitted changes — the harden "
            "worktree is created from HEAD, so they are NOT part of the measured harness"
        )
    subprocess.run(
        ["git", "-C", repo_dir, "worktree", "add", "-q", "-b", branch, str(path)],
        check=True, capture_output=True, text=True,
    )
    log(f"running in auto-worktree [bold]{path}[/bold] (branch {branch})")
    return str(path), branch


def _cleanup_worktree(invoking_repo: str, repo_dir: str, branch: str) -> None:
    """Drop an auto-worktree that produced no commits (best-effort)."""
    subprocess.run(
        ["git", "-C", invoking_repo, "worktree", "remove", "--force", repo_dir],
        capture_output=True,
    )
    subprocess.run(["git", "-C", invoking_repo, "branch", "-D", branch], capture_output=True)


def _promote(invoking_repo: str, branch: str, log) -> bool:
    """Cherry-pick the run's champion commits onto the invoking checkout.

    Returns True when every commit landed. Refuses on a dirty checkout (a
    half-applied cherry-pick over local edits is worse than a manual step) and
    aborts cleanly on conflicts, leaving the harden branch untouched either way.
    """
    dirty = subprocess.run(
        ["git", "-C", invoking_repo, "status", "--porcelain"],
        text=True, capture_output=True,
    ).stdout.strip()
    if dirty:
        log(
            "[yellow]promote skipped[/yellow]: your checkout has uncommitted changes — "
            f"bring the run in yourself with: git cherry-pick ..{branch}"
        )
        return False

    base = subprocess.run(
        ["git", "-C", invoking_repo, "merge-base", "HEAD", branch],
        text=True, capture_output=True,
    ).stdout.strip()
    if not base:
        log(f"[yellow]promote skipped[/yellow]: no merge base with {branch}")
        return False

    picked = subprocess.run(
        ["git", "-C", invoking_repo, "cherry-pick", f"{base}..{branch}"],
        text=True, capture_output=True,
    )
    if picked.returncode != 0:
        subprocess.run(
            ["git", "-C", invoking_repo, "cherry-pick", "--abort"], capture_output=True
        )
        tail = " ".join((picked.stderr or picked.stdout).split())[-300:]
        log(
            f"[yellow]promote failed[/yellow] (cherry-pick conflict, aborted cleanly): {tail} — "
            f"the run is intact on {branch}; resolve manually with: git cherry-pick {base[:12]}..{branch}"
        )
        return False

    count = subprocess.run(
        ["git", "-C", invoking_repo, "rev-list", "--count", f"{base}..{branch}"],
        text=True, capture_output=True,
    ).stdout.strip()
    log(f"[bold green]promoted {count} champion commit(s) onto your checkout[/bold green]")
    return True


def main() -> None:
    ap = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    ap.add_argument(
        "--cases",
        default="",
        help="restrict to one cases/*.yaml file. The default is EVERY case in cases/ "
        "(the full hydrated suite): champions must generalize across every workload at "
        "once, and there are enough questions to afford --held-out (recommended: 3-4 "
        "questions the proposer never sees). Use --tags (or a single file) for quick "
        "targeted iterations.",
    )
    ap.add_argument(
        "--tags",
        action="append",
        default=[],
        help="run only cases carrying at least one of these tags (repeatable, comma-ok)",
    )
    ap.add_argument(
        "--exclude-tags",
        action="append",
        default=[],
        help="drop cases carrying any of these tags (repeatable, comma-ok)",
    )
    ap.add_argument(
        "--subject", action="append", default=[], help="provider/model:route (repeatable)"
    )
    ap.add_argument("--proposer-model", default="gpt-5.5")
    ap.add_argument("--reasoning-effort", default="xhigh")
    ap.add_argument(
        "--auditor-model",
        default="gpt-5.5",
        help="model for the adversarial diff auditor (fresh context; xhigh reasoning)",
    )
    ap.add_argument("--no-audit", action="store_true", help="disable the adversarial auditor stage")
    ap.add_argument("--judge-model", default=DEFAULT_EVALUATOR_MODEL)
    ap.add_argument(
        "--grader",
        default="",
        help="promptfoo grading provider for llm-rubric asserts (default: <judge-model> via opencode-go)",
    )
    ap.add_argument("--rounds", type=int, default=3)
    ap.add_argument(
        "--k",
        type=int,
        default=3,
        help="runs per (question, subject) — averages out effort variance",
    )
    ap.add_argument("--show", type=int, default=12, help="how many worst runs to show the proposer")
    ap.add_argument(
        "--subject-timeout",
        type=float,
        default=1800.0,
        help="per-question agent timeout (seconds). Generous on purpose: a flailing "
        "agent should be MEASURED (it burns tokens and scores low), not crashed "
        "(score-0 noise that contaminates the gates).",
    )
    ap.add_argument("--proposer-timeout", type=float, default=1800.0)
    ap.add_argument("--port", type=int, default=2481, help="scratch panda-server port")
    ap.add_argument("--concurrency", type=int, default=16, help="max agent runs in flight at once")
    ap.add_argument(
        "--question-id",
        action="append",
        default=[],
        help="restrict to specific case id(s) (repeatable); default = all in --cases",
    )
    ap.add_argument(
        "--min-cells",
        type=int,
        default=3,
        help="min (question, subject) cells for the confidence gate; set 1 for a single-question smoke",
    )
    ap.add_argument(
        "--held-out",
        action="append",
        default=[],
        help="case id(s) the proposer never sees; the confidence gate is computed on these "
        "(anti-overfit). Repeatable. Without it, the gate runs on all questions and can be gamed.",
    )
    ap.add_argument(
        "--sandbox",
        action=argparse.BooleanOptionalAction,
        default=True,
        help="run the subject in a container with no repo access, so it can't read the eval "
        "cases or source (it sees only a linux `panda` + the scratch server). On by "
        "default; --no-sandbox runs the agent on the host (it can read your whole disk).",
    )
    ap.add_argument(
        "--in-place",
        action="store_true",
        help="run in the current checkout instead of an auto-created worktree. The loop "
        "reverts/commits the tree it runs in, and any edits made to it mid-run abort the "
        "run — in-place means nobody can touch the checkout until it finishes.",
    )
    ap.add_argument(
        "--pool-size",
        type=int,
        default=6,
        help="max candidate states kept as mutation parents (GEPA-style Pareto pool)",
    )
    ap.add_argument(
        "--prescreen",
        type=int,
        default=3,
        help="functional smoke: k=1 on this many of the parent's worst questions before "
        "the full measure, rejecting only candidates with zero correct runs where the "
        "parent worked (0 disables)",
    )
    ap.add_argument(
        "--promote",
        action=argparse.BooleanOptionalAction,
        default=True,
        help="on success, cherry-pick the champion commit(s) onto the checkout the run "
        "was launched from (skipped if it has uncommitted changes); --no-promote "
        "leaves them only on the harden/<ts> branch",
    )
    ap.add_argument(
        "--audit-retries",
        type=int,
        default=3,
        help="how many times a blocked proposal goes back to the proposer with the "
        "auditor's findings to amend (0 = a block is final)",
    )
    args = ap.parse_args()
    log = setup_logging().info

    ts = time.strftime("%Y-%m-%dT%H-%M-%S")
    invoking_repo = _repo_root()
    branch = None
    if args.in_place:
        repo_dir = invoking_repo
    else:
        # The run gets its OWN worktree from HEAD: the loop's reverts/commits never
        # touch the user's checkout, and edits to the checkout can't corrupt the run.
        repo_dir, branch = _auto_worktree(invoking_repo, ts, log)

    tags = [t for v in args.tags for t in v.split(",") if t.strip()]
    exclude_tags = [t for v in args.exclude_tags for t in v.split(",") if t.strip()]
    selection = args.cases or "all files"
    if tags:
        selection += f" tags={','.join(tags)}"
    if exclude_tags:
        selection += f" exclude={','.join(exclude_tags)}"
    questions = [
        Question(
            id=c.id, text=c.input, followups=c.followups, asserts=c.asserts, variations=c.variations
        )
        for c in load_test_cases(args.cases or None, tags=tags, exclude_tags=exclude_tags)
    ]
    if args.question_id:
        wanted = set(args.question_id)
        questions = [q for q in questions if q.id in wanted]
    if not questions:
        raise SystemExit(f"no cases matched ({selection})")

    # Local scratch server built from the candidate source; CLI subjects hit it via
    # PANDA_CONFIG + the freshly-built `panda` on PATH (set before any subject spawns).
    config_path = write_scratch_config(args.port)
    point_cli_at_scratch(repo_dir, config_path)
    server = ScratchServer(repo_dir, config_path, args.port)
    if args.sandbox:
        prepare_opencode_sandbox(repo_dir, args.port)
    apply = make_apply(server, sandbox=args.sandbox)

    # promptfoo runs the subjects in a python worker; point it at THIS venv so it can import
    # the agent stack. Langfuse falls out for free: the worker inherits this process's env,
    # so if LANGFUSE_ENABLED + keys are set (as in the smoke CI), each run is pushed to
    # Langfuse production by the agent itself — humans inspect there, the proposer reads the
    # full traces on disk.
    os.environ.setdefault("PROMPTFOO_PYTHON", sys.executable)
    eval_dir = str(Path(__file__).resolve().parents[1])

    subject_specs = args.subject or DEFAULT_SUBJECTS
    grader = args.grader or grader_for(args.judge_model)
    proposer = CodexProposer(
        repo_dir,
        model=args.proposer_model,
        reasoning_effort=args.reasoning_effort,
        timeout=args.proposer_timeout,
        log=log,
    )
    auditor = (
        None
        if args.no_audit
        else CodexAuditor(
            repo_dir,
            model=args.auditor_model,
            reasoning_effort=args.reasoning_effort,
            log=log,
        )
    )

    run_dir = HARDEN_HOME / "runs" / ts
    qids = ", ".join(q.id for q in questions)
    nvar = sum(len(q.variations) for q in questions)
    auditor_desc = "off" if args.no_audit else f"{args.auditor_model} @ {args.reasoning_effort} (codex)"
    banner = [
        "=== harden config ===",
        f"  subjects (agent): {', '.join(subject_specs)}"
        + ("   [sandboxed: container, no repo access]" if args.sandbox else "   [host process]"),
        f"  grader:           {grader}",
        f"  proposer:         {args.proposer_model} @ {args.reasoning_effort} (codex)",
        f"  auditor:          {auditor_desc}",
        f"  questions:        {len(questions)} ({qids}) + {nvar} variations | k={args.k} | "
        f"rounds={args.rounds} | min-cells={args.min_cells}"
        + (f" | held-out={sorted(args.held_out)}" if args.held_out else ""),
        f"  scratch server:   :{args.port}",
        f"  artifacts:        {run_dir}",
        "=====================",
    ]
    log("[bold cyan]" + "\n".join(banner) + "[/bold cyan]")
    try:
        result = asyncio.run(
            optimize(
                questions,
                subject_specs,
                proposer,
                repo_dir=repo_dir,
                apply=apply,
                auditor=auditor,
                k=args.k,
                rounds=args.rounds,
                show=args.show,
                min_cells=args.min_cells,
                concurrency=args.concurrency,
                grader=grader,
                subject_timeout=int(args.subject_timeout),
                cwd=eval_dir,
                held_out_ids=set(args.held_out) or None,
                save_dir=str(run_dir),
                pool_size=args.pool_size,
                prescreen=args.prescreen,
                audit_retries=args.audit_retries,
                journal=Journal(HARDEN_HOME / JOURNAL_NAME, context=selection),
                log=log,
            )
        )
    finally:
        server.stop()
    log(f"[bold]=== {result.accepted}/{len(result.rounds)} rounds accepted ===[/bold]")
    for r in result.rounds:
        flag = "[green]ACCEPT[/green]" if r.accepted else f"[yellow]reject:{r.reason}[/yellow]"
        log(f"  round {r.n}: {flag}  score {r.score_before:.3f} -> {r.score_after:.3f}")
    if branch is not None:
        if result.accepted:
            sha = subprocess.run(
                ["git", "-C", repo_dir, "rev-parse", "--short", "HEAD"],
                text=True, capture_output=True,
            ).stdout.strip()
            log(f"[bold green]committed on {branch}[/bold green] (worktree {repo_dir})")
            promoted = args.promote and _promote(invoking_repo, branch, log)
            if promoted:
                _cleanup_worktree(invoking_repo, repo_dir, branch)
                log("auto-worktree removed (champions promoted)")
            else:
                log(f"bring it into your checkout with: git cherry-pick {sha}")
        else:
            _cleanup_worktree(invoking_repo, repo_dir, branch)
            log(f"auto-worktree removed (nothing was committed); branch {branch} deleted")


if __name__ == "__main__":
    main()
