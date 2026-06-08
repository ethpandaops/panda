"""Run the harden optimization loop against the panda harness.

    uv run python -m scripts.harden \
        --cases smoke.yaml --rounds 3 --k 3 --budget 20000 \
        --subject opencode-go/deepseek-v4-flash:cli \
        --subject opencode/gpt-5.4-mini:cli

The loop measures the current harness, lets Codex (GPT-5.5 @ xhigh) edit panda from the
RAW agent traces, rebuilds + re-measures, and keeps the change only if it doesn't regress
correctness and is bootstrap-confidently better. It commits accepted changes and
git-reverts rejected ones, so run it on a throwaway worktree/branch with a clean tree.

It builds panda from the candidate source and runs it as a LOCAL scratch server (default
:2481), derived from your ~/.config/panda/config.yaml (hosted proxy + datasources work as
is; sandbox callbacks go via host.docker.internal). Embeddings cache under ~/.panda/harden
so restarts are ~7s and offline. Go edits rebuild+restart the server; sandbox-API edits
rebuild the image and are picked up live. Your real stack on :2480 is untouched.
"""

from __future__ import annotations

import argparse
import asyncio
import subprocess
import time

from cases.loader import load_test_cases
from config.settings import DEFAULT_EVALUATOR_MODEL
from harden.auditor import CodexAuditor
from harden.judge import Judge
from harden.loop import optimize
from harden.proposer import CodexProposer
from harden.runner import Question
from harden.subject import OpencodeSubject
from scripts._panda_env import (
    HARDEN_HOME,
    ScratchServer,
    make_apply,
    point_cli_at_scratch,
    write_scratch_config,
)


def _repo_root() -> str:
    return subprocess.run(
        ["git", "rev-parse", "--show-toplevel"], text=True, capture_output=True, check=True
    ).stdout.strip()


def _subject(spec: str, timeout: float) -> OpencodeSubject:
    """``provider/model:route`` -> OpencodeSubject (route defaults to cli)."""
    model, _, route = spec.partition(":")
    return OpencodeSubject(model=model, route=route or "cli", timeout=timeout)


def main() -> None:
    ap = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    ap.add_argument("--cases", default="smoke.yaml", help="cases/*.yaml to use as the question set")
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
    ap.add_argument("--rounds", type=int, default=3)
    ap.add_argument(
        "--k",
        type=int,
        default=3,
        help="runs per (question, subject) — averages out effort variance",
    )
    ap.add_argument(
        "--budget", type=int, default=20000, help="token efficiency knee (a 'good' run's cost)"
    )
    ap.add_argument("--show", type=int, default=12, help="how many worst runs to show the proposer")
    ap.add_argument("--subject-timeout", type=float, default=180.0)
    ap.add_argument("--proposer-timeout", type=float, default=1800.0)
    ap.add_argument("--port", type=int, default=2481, help="scratch panda-server port")
    ap.add_argument("--concurrency", type=int, default=6, help="max agent runs in flight at once")
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
    args = ap.parse_args()

    repo_dir = _repo_root()
    questions = [
        Question(
            id=c.id,
            text=c.input,
            reference=c.reference,
            reference_query=c.reference_query,
            reference_query_datasource=c.reference_query_datasource,
        )
        for c in load_test_cases(args.cases)
    ]
    if args.question_id:
        wanted = set(args.question_id)
        questions = [q for q in questions if q.id in wanted]
    if not questions:
        raise SystemExit(f"no questions loaded from cases/{args.cases}")

    # Local scratch server built from the candidate source; CLI subjects hit it via
    # PANDA_CONFIG + the freshly-built `panda` on PATH (set before any subject spawns).
    config_path = write_scratch_config(args.port)
    point_cli_at_scratch(repo_dir, config_path)
    server = ScratchServer(repo_dir, config_path, args.port)
    apply = make_apply(server)

    def log(m: str) -> None:
        print(m, flush=True)

    subject_specs = args.subject or ["opencode-go/deepseek-v4-flash:cli"]
    subjects = [_subject(s, args.subject_timeout) for s in subject_specs]
    judge = Judge(args.judge_model)
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

    run_dir = HARDEN_HOME / "runs" / time.strftime("%Y-%m-%dT%H-%M-%S")
    print(
        f"harden: {len(questions)} questions x {len(subjects)} subjects x k={args.k} "
        f"| proposer={args.proposer_model}@{args.reasoning_effort} | rounds={args.rounds} "
        f"| scratch server :{args.port}\nartifacts: {run_dir}",
        flush=True,
    )
    try:
        result = asyncio.run(
            optimize(
                questions,
                subjects,
                judge,
                proposer,
                repo_dir=repo_dir,
                apply=apply,
                budget=args.budget,
                auditor=auditor,
                k=args.k,
                rounds=args.rounds,
                show=args.show,
                min_cells=args.min_cells,
                concurrency=args.concurrency,
                held_out_ids=set(args.held_out) or None,
                save_dir=str(run_dir),
                log=log,
            )
        )
    finally:
        server.stop()
    print(f"\n=== {result.accepted}/{len(result.rounds)} rounds accepted ===")
    for r in result.rounds:
        flag = "ACCEPT" if r.accepted else f"reject:{r.reason}"
        print(f"  round {r.n}: {flag}  score {r.score_before:.3f} -> {r.score_after:.3f}")


if __name__ == "__main__":
    main()
