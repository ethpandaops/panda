"""The optimization loop — measure, propose, re-measure, gate. Deliberately dumb.

    baseline = measure(current harness)
    repeat:
        proposer edits the harness from the baseline's RAW traces
        apply()                       # rebuild + restart — injected, env-specific
        candidate = measure()
        if no correctness regression AND confidently better:  accept (commit), rebase baseline
        else:                                                 revert (git) + rebuild

The loop knows nothing panda-specific or env-specific. It decides accept/reject from the
two top-level gates in scoring.py and nothing else; the proposer reasons from raw traces;
"how to make an edit live" is the injected ``apply`` callable. Swap the Subject, the
Proposer, or the apply command and the loop is unchanged.

Safety: the loop refuses to start unless the git tree is CLEAN, so a rejected proposal is
reverted with a full ``git checkout`` + ``git clean`` back to that known-good commit —
no chance of clobbering uncommitted work. Run it on a throwaway worktree/branch.
"""

from __future__ import annotations

import subprocess
from collections.abc import Callable
from dataclasses import dataclass, field
from pathlib import Path

from harden.judge import Judge
from harden.proposer import Proposer
from harden.report import build_proposal_prompt, format_record
from harden.runner import CandidateResult, Question, run_candidate
from harden.scoring import is_confident, no_correctness_regression
from harden.subject import Subject


def _dump(save_dir: str | None, name: str, text: str) -> None:
    """Write a debugging artifact (raw traces / proposal prompt) if save_dir is set."""
    if not save_dir:
        return
    d = Path(save_dir)
    d.mkdir(parents=True, exist_ok=True)
    (d / name).write_text(text)


def _dump_records(save_dir: str | None, name: str, result: CandidateResult) -> None:
    if not save_dir:
        return
    body = "\n".join(format_record(r) for r in result.records)
    _dump(save_dir, name, f"score={result.score:.3f} pass={result.pass_rate:.2f}\n\n{body}")


@dataclass
class Round:
    n: int
    accepted: bool
    reason: str
    score_before: float
    score_after: float
    pass_before: float
    pass_after: float
    proposal_summary: str = ""


@dataclass
class OptimizeResult:
    baseline: CandidateResult
    rounds: list[Round] = field(default_factory=list)

    @property
    def accepted(self) -> int:
        return sum(1 for r in self.rounds if r.accepted)


def _git(repo: str, *args: str) -> str:
    return subprocess.run(
        ["git", "-C", repo, *args], text=True, capture_output=True, check=True
    ).stdout.strip()


def _is_clean(repo: str) -> bool:
    return _git(repo, "status", "--porcelain") == ""


def _revert(repo: str) -> None:
    """Discard ALL working-tree changes back to HEAD. Safe only on a clean-start tree:
    tracked edits are checked out, untracked (non-ignored) new files are cleaned; ignored
    build artifacts are left alone (no ``-x``)."""
    _git(repo, "checkout", "--", ".")
    _git(repo, "clean", "-fd")


def _commit(repo: str, message: str) -> None:
    _git(repo, "add", "-A")
    _git(repo, "commit", "-m", message, "--no-verify")


async def optimize(
    questions: list[Question],
    subjects: list[Subject],
    judge: Judge,
    proposer: Proposer,
    *,
    repo_dir: str,
    apply: Callable[[], None],
    budget: int,
    k: int = 3,
    rounds: int = 5,
    show: int = 12,
    steepness: float = 2.0,
    min_cells: int = 3,
    concurrency: int = 6,
    save_dir: str | None = None,
    log: Callable[[str], None] = print,
) -> OptimizeResult:
    """Run the optimization loop. ``apply`` rebuilds+restarts the harness so a fresh
    build is live; it must raise on failure. ``budget`` is the token-efficiency knee."""
    if not _is_clean(repo_dir):
        raise RuntimeError(
            f"{repo_dir} has uncommitted changes; the loop reverts with git and would "
            "clobber them. Commit or stash first (run on a throwaway worktree/branch)."
        )

    def _on_run(q: Question, rs, _trace) -> None:
        log(
            f"    · {rs.subject} q={q.id} correct={rs.correct} "
            f"tokens={rs.tokens} tools={rs.n_tools} score={rs.score:.2f}"
        )

    async def measure(label: str) -> CandidateResult:
        n = len(questions) * len(subjects) * k
        log(
            f"  measuring {label}: {n} runs ({len(subjects)} subj x k={k}), up to {concurrency} at once..."
        )
        return await run_candidate(
            questions,
            subjects,
            judge,
            k=k,
            budget=budget,
            steepness=steepness,
            concurrency=concurrency,
            on_run=_on_run,
        )

    log("rebuilding harness (baseline)...")
    apply()
    baseline = await measure("baseline")
    log(f"baseline: score={baseline.score:.3f} pass={baseline.pass_rate:.2f}")
    _dump_records(save_dir, "baseline.txt", baseline)
    result = OptimizeResult(baseline=baseline)

    for n in range(1, rounds + 1):
        log(f"--- round {n}/{rounds} ---")
        prompt = build_proposal_prompt(baseline.records, limit=show)
        _dump(save_dir, f"round{n}_proposal_prompt.txt", prompt)
        log(f"round {n}: proposing harness edits (this can take several minutes)...")
        proposal = proposer.propose(prompt)
        _dump(save_dir, f"round{n}_proposal_summary.txt", proposal.summary)
        if not proposal.ok:
            log(f"round {n}: proposer failed: {proposal.summary[:200]}")
            _revert(repo_dir)
            result.rounds.append(
                _round(n, False, "proposer-failed", baseline, baseline, proposal.summary)
            )
            continue
        if _is_clean(repo_dir):
            log(f"round {n}: proposer made no edits")
            result.rounds.append(_round(n, False, "no-edits", baseline, baseline, proposal.summary))
            continue

        log(f"round {n}: proposal made edits; rebuilding + restarting harness...")
        try:
            apply()
        except Exception as exc:  # noqa: BLE001 - a broken build is a rejected proposal
            log(f"round {n}: build failed, reverting: {type(exc).__name__}: {exc}")
            _revert(repo_dir)
            apply()
            result.rounds.append(
                _round(n, False, "build-failed", baseline, baseline, proposal.summary)
            )
            continue

        candidate = await measure("candidate")
        _dump_records(save_dir, f"round{n}_candidate.txt", candidate)
        regressed = not no_correctness_regression(baseline.runs, candidate.runs)
        confident = is_confident(baseline.runs, candidate.runs, min_cells=min_cells)
        if not regressed and confident:
            _commit(repo_dir, f"harden round {n}: {baseline.score:.3f} -> {candidate.score:.3f}")
            log(
                f"round {n}: ACCEPT score {baseline.score:.3f} -> {candidate.score:.3f} "
                f"pass {baseline.pass_rate:.2f} -> {candidate.pass_rate:.2f}"
            )
            result.rounds.append(_round(n, True, "accepted", baseline, candidate, proposal.summary))
            baseline = candidate
        else:
            reason = "regressed-correctness" if regressed else "not-confident"
            log(
                f"round {n}: REJECT ({reason}) score {baseline.score:.3f} -> "
                f"{candidate.score:.3f} pass {baseline.pass_rate:.2f} -> {candidate.pass_rate:.2f}"
            )
            _revert(repo_dir)
            apply()
            result.rounds.append(_round(n, False, reason, baseline, candidate, proposal.summary))

    log(f"done: {result.accepted}/{rounds} accepted, final score {baseline.score:.3f}")
    return result


def _round(
    n: int,
    accepted: bool,
    reason: str,
    before: CandidateResult,
    after: CandidateResult,
    summary: str,
) -> Round:
    return Round(
        n=n,
        accepted=accepted,
        reason=reason,
        score_before=before.score,
        score_after=after.score,
        pass_before=before.pass_rate,
        pass_after=after.pass_rate,
        proposal_summary=summary[:500],
    )
