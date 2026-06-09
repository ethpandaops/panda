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
import tempfile
from collections.abc import Callable
from dataclasses import dataclass, field
from pathlib import Path

from harden.auditor import Auditor
from harden.promptfoo_eval import measure_candidate
from harden.proposer import Proposer
from harden.report import build_proposal_prompt
from harden.runner import CandidateResult, Question
from harden.scoring import filter_runs, is_confident, no_correctness_regression


def _dump(save_dir: str | None, name: str, text: str) -> None:
    """Write a debugging artifact (proposal prompt / summary) if save_dir is set."""
    if not save_dir:
        return
    d = Path(save_dir)
    d.mkdir(parents=True, exist_ok=True)
    (d / name).write_text(text)


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


def _proposal_diff(repo: str) -> str:
    """The proposal's full diff vs HEAD, including untracked new files — what the auditor
    reviews. Does not stage anything (so revert stays a plain checkout + clean)."""
    parts = []
    tracked = _git(repo, "diff", "HEAD")
    if tracked:
        parts.append(tracked)
    for path in _git(repo, "ls-files", "--others", "--exclude-standard").splitlines():
        path = path.strip()
        if not path:
            continue
        try:
            content = Path(repo, path).read_text(errors="replace")
        except OSError:
            continue
        parts.append(f"--- /dev/null\n+++ b/{path}\n{content}")
    return "\n".join(parts)


async def optimize(
    questions: list[Question],
    subject_specs: list[str],
    proposer: Proposer,
    *,
    repo_dir: str,
    apply: Callable[[], None],
    budget: int,
    auditor: Auditor | None = None,
    k: int = 3,
    rounds: int = 5,
    show: int = 12,
    steepness: float = 2.0,
    min_cells: int = 3,
    concurrency: int = 6,
    grader: str = "openrouter:google/gemini-3.1-flash-lite",
    subject_timeout: int = 300,
    cwd: str | None = None,
    held_out_ids: set[str] | None = None,
    save_dir: str | None = None,
    log: Callable[[str], None] = print,
) -> OptimizeResult:
    """Run the optimization loop. ``apply`` rebuilds+restarts the harness so a fresh
    build is live; it must raise on failure. ``budget`` is the token-efficiency knee.

    ``held_out_ids`` is the anti-overfit gate: questions in this set are NEVER shown to the
    proposer (the prompt is built from train traces only), and the confidence gate is
    computed on these held-out questions. A change that just memorizes the train questions
    produces no held-out gain and is rejected. The no-correctness-regression floor still
    applies to ALL questions. With no held_out_ids the loop gates on everything (fine for a
    single-question smoke, but it CAN be gamed by encoding that question's answer)."""
    if not _is_clean(repo_dir):
        raise RuntimeError(
            f"{repo_dir} has uncommitted changes; the loop reverts with git and would "
            "clobber them. Commit or stash first (run on a throwaway worktree/branch)."
        )
    held_out_ids = held_out_ids or set()
    train_ids = {q.id for q in questions} - held_out_ids
    if held_out_ids:
        log(f"train questions: {sorted(train_ids)} | held-out (gate): {sorted(held_out_ids)}")

    run_root = Path(save_dir) if save_dir else Path(tempfile.mkdtemp(prefix="harden-"))

    async def measure(label: str) -> CandidateResult:
        n = len(questions) * len(subject_specs) * k
        log(f"  measuring {label}: {n} runs ({len(subject_specs)} subj x k={k}) via promptfoo...")
        return await measure_candidate(
            questions,
            subject_specs,
            k=k,
            budget=budget,
            run_dir=str(run_root / label),
            steepness=steepness,
            grader=grader,
            concurrency=concurrency,
            subject_timeout=subject_timeout,
            cwd=cwd,
        )

    log("rebuilding harness (baseline)...")
    apply()
    baseline = await measure("baseline")
    log(f"baseline: score={baseline.score:.3f} pass={baseline.pass_rate:.2f}")
    baseline_traces = run_root / "baseline" / "traces"
    result = OptimizeResult(baseline=baseline)

    for n in range(1, rounds + 1):
        log(f"--- round {n}/{rounds} ---")
        # The proposer only ever sees TRAIN traces — never the held-out questions. The
        # prompt is a lean summary; the FULL traces live in baseline_traces for it to read.
        train_records = [r for r in baseline.records if r.question.id in train_ids]
        prompt = build_proposal_prompt(train_records, traces_dir=str(baseline_traces), limit=show)
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

        # Adversarial audit BEFORE the expensive build+measure: a fresh-context reviewer
        # tries to refuse the diff for answer-leakage / misplacement / eval-infra gaming —
        # the cheats the held-out gate can't see. Blocked -> revert without measuring.
        if auditor is not None:
            log(f"round {n}: auditing the proposed diff...")
            verdict = auditor.audit(_proposal_diff(repo_dir), [q.text for q in questions])
            _dump(save_dir, f"round{n}_audit.txt", verdict.text())
            if verdict.blocked:
                log(f"round {n}: AUDIT BLOCKED — {verdict.summary[:200]}")
                _revert(repo_dir)
                result.rounds.append(
                    _round(n, False, "audit-blocked", baseline, baseline, verdict.text())
                )
                continue
            if verdict.findings:
                log(
                    f"round {n}: audit passed with {len(verdict.findings)} warning(s) (see artifacts)"
                )

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

        candidate = await measure(f"round{n}_candidate")
        # Correctness must not regress ANYWHERE; the improvement must show on the HELD-OUT
        # questions the proposer never saw (so memorizing the train questions can't pass).
        gate_base = filter_runs(baseline.runs, held_out_ids) if held_out_ids else baseline.runs
        gate_cand = filter_runs(candidate.runs, held_out_ids) if held_out_ids else candidate.runs
        gate_label = "held-out" if held_out_ids else "all"
        regressed = not no_correctness_regression(baseline.runs, candidate.runs)
        confident = is_confident(gate_base, gate_cand, min_cells=min_cells)
        log(f"round {n}: gate on {gate_label} — regressed={regressed} confident={confident}")
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
