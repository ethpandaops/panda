"""The optimization loop — measure, propose, prescreen, re-measure, gate.

    baseline = measure(current harness)
    pool = {baseline}                          # candidate states, as patches vs HEAD
    repeat:
        parent = pick from pool (weighted by per-cell wins — GEPA-style Pareto)
        proposer edits the harness from the PARENT's raw traces (eval cases hidden)
        deterministic guards -> LLM audit -> apply() (rebuild + restart)
        prescreen on the parent's worst questions (k=1, cheap) — bail early if broken
        candidate = measure()
        gates vs the ORIGINAL baseline:
            correctness regressed             -> discard
            confidently better (held-out)     -> champion (+ pool)
            strictly best on >=1 cell in pool -> pool (mutation parent only)
    finally: commit the best champion's patch (one commit), or nothing.

Single-lineage hill-climbing is the one configuration GEPA ablates and beats (+6.4
aggregate): a greedy loop finds one idea and grinds on it. The pool keeps every state
that is best at SOMETHING as a future mutation parent, while the COMMIT bar stays what
it always was — no correctness regression anywhere, confidently better on questions the
proposer never saw.

The loop knows nothing panda-specific or env-specific. It decides accept/reject from the
two top-level gates in scoring.py and nothing else; the proposer reasons from raw traces;
"how to make an edit live" is the injected ``apply`` callable. Swap the Subject, the
Proposer, or the apply command and the loop is unchanged.

Safety: the loop refuses to start unless the git tree is CLEAN, so candidate states are
plain patches vs HEAD and any reject is a full ``git checkout`` + ``git clean`` back to
that known-good commit — no chance of clobbering uncommitted work. HEAD never moves until
the single end-of-run champion commit. Run it on a throwaway worktree/branch.
"""

from __future__ import annotations

import contextlib
import random
import re
import shutil
import statistics
import subprocess
import tempfile
from collections.abc import Callable, Iterator
from dataclasses import dataclass, field, replace
from pathlib import Path

from rich.markup import escape

from config.settings import DEFAULT_GRADER
from harden import charts
from harden.auditor import Auditor
from harden.journal import Journal, patch_fingerprint
from harden.promptfoo_eval import measure_candidate
from harden.proposer import Proposer
from harden.report import build_amend_prompt, build_proposal_prompt
from harden.runner import CandidateResult, Question
from harden.scoring import filter_runs, is_confident, no_correctness_regression


def _dump(save_dir: str | None, name: str, text: str) -> None:
    """Write a debugging artifact (proposal prompt / summary) if save_dir is set."""
    if not save_dir:
        return
    d = Path(save_dir)
    d.mkdir(parents=True, exist_ok=True)
    (d / name).write_text(text)


def _log_breakdown(log: Callable[[str], None], label: str, result: CandidateResult) -> None:
    """Per-question detail under an aggregate measure: correct/total, score, and the token
    SPREAD of the correct runs (best / typical / worst). The best correct run is an existence
    proof of what's achievable — best-vs-typical is the proven, capturable headroom (e.g. the
    agent CAN do it in 9k but typically burns 18k -> make the lean path the default)."""
    by_q: dict[str, list] = {}
    for r in result.records:
        by_q.setdefault(r.score.question_id, []).append(r.score)
    log(f"  {label}: score={result.score:.3f} pass={result.pass_rate:.2f} ({len(result.records)} runs)")
    for qid, scores in sorted(by_q.items()):
        passed = sum(1 for s in scores if s.correct)
        tools = statistics.mean(s.n_tools for s in scores)
        sc = statistics.mean(s.score for s in scores)
        ct = sorted(s.tokens for s in scores if s.correct)
        if ct:
            best, typ, worst = ct[0], int(statistics.median(ct)), ct[-1]
            head = f" | tok best {best} / typ {typ} / worst {worst} (headroom {typ / best:.1f}x)"
        else:
            head = " | (no correct runs)"
        log(f"    {qid:30} {passed}/{len(scores)} ok | {tools:.1f} tools | score {sc:.2f}{head}")


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


def _changed_paths(repo: str) -> list[str]:
    """Every path the proposal touched: tracked edits + untracked new files."""
    tracked = _git(repo, "diff", "HEAD", "--name-only").splitlines()
    untracked = _git(repo, "ls-files", "--others", "--exclude-standard").splitlines()
    return [p.strip() for p in tracked + untracked if p.strip()]


def _protected_violations(repo: str, protected: tuple[str, ...]) -> list[str]:
    """Touched paths under a protected prefix. The prompt TELLS the proposer to keep out
    of the eval harness and CI; this enforces it deterministically — an LLM auditor can
    be argued past, a path prefix can't. Editing the rubrics that grade you, or the
    workflow that runs you, is never a legitimate proposal."""
    return [p for p in _changed_paths(repo) if any(p.startswith(pre) for pre in protected)]


_IDENT = re.compile(r"\b[a-z][a-z0-9]*(?:_[a-z0-9]+)+\b")  # snake_case identifiers
_NUMBER = re.compile(r"\b\d{2,}(?:-\d{2,})?%?\b")  # multi-digit numbers / ranges (85-95%)


def _leak_check(diff: str, questions: list[Question]) -> list[str]:
    """Mechanical (ungameable) leakage screen: distinctive tokens from the GRADING
    rubrics — identifiers and numeric ranges, the parts that encode what a correct
    answer looks like — appearing verbatim in lines the proposal ADDS. The LLM auditor
    judges intent; this catches the literal cheat regardless of how it's dressed up."""
    tokens: set[str] = set()
    for q in questions:
        for a in q.asserts:
            text = str(a.get("value", ""))
            tokens.update(_IDENT.findall(text))
            tokens.update(_NUMBER.findall(text))
    tokens.discard("")
    if not tokens:
        return []
    hits = []
    for line in diff.splitlines():
        if not line.startswith("+"):
            continue
        for t in tokens:
            if t in line:
                hits.append(f"rubric token {t!r} in added line: {line[:160]}")
    return hits


@contextlib.contextmanager
def _hidden(paths: list[Path]) -> Iterator[None]:
    """Information barrier: move ``paths`` out of the repo while the proposer runs, so
    it cannot read the eval cases/rubrics (held-out questions, expected-answer ranges)
    even though it has unrestricted file access. Restored on exit — including when the
    proposer crashes. Anything the proposer re-creates at a hidden path is discarded on
    restore (it's a protected path; such an edit would be rejected anyway)."""
    moved: list[tuple[Path, Path]] = []
    tmp = Path(tempfile.mkdtemp(prefix="harden-hidden-"))
    try:
        for i, p in enumerate(paths):
            if p.exists():
                stash = tmp / f"{i}_{p.name}"
                shutil.move(str(p), str(stash))
                moved.append((p, stash))
        yield
    finally:
        for orig, stash in moved:
            if orig.exists():  # proposer re-created it — discard in favor of the original
                shutil.rmtree(orig, ignore_errors=True) if orig.is_dir() else orig.unlink()
            shutil.move(str(stash), str(orig))
        shutil.rmtree(tmp, ignore_errors=True)


def _capture_patch(repo: str) -> str:
    """The working tree's full diff vs HEAD as a real, re-appliable git patch (new files
    included). Stages everything to render the patch, then unstages — the tree itself is
    untouched, so revert stays a plain checkout + clean. NB: not via ``_git`` — that
    strips whitespace, and git refuses a patch missing its trailing newline."""
    _git(repo, "add", "-A")
    patch = subprocess.run(
        ["git", "-C", repo, "diff", "--cached", "--binary"],
        text=True, capture_output=True, check=True,
    ).stdout
    _git(repo, "reset", "-q")
    return patch


def _apply_patch(repo: str, patch: str) -> None:
    """Materialize a captured patch onto the clean HEAD tree."""
    if not patch:
        return
    subprocess.run(
        ["git", "-C", repo, "apply", "--binary", "--whitespace=nowarn"],
        input=patch, text=True, capture_output=True, check=True,
    )


@dataclass
class PoolEntry:
    """One harness state in the candidate pool: its patch vs HEAD + its measurement.
    ``champion`` marks states that passed the COMMIT bar (confidently better than the
    original baseline on the gate questions); non-champions are mutation parents only."""

    label: str
    patch: str  # "" = the baseline itself
    result: CandidateResult
    traces_dir: str
    champion: bool = False


def _cell_means(result: CandidateResult) -> dict[tuple[str, str], float]:
    cells: dict[tuple[str, str], list[float]] = {}
    for rs in result.runs:
        cells.setdefault((rs.question_id, rs.subject), []).append(rs.score)
    return {c: statistics.mean(v) for c, v in cells.items()}


def _pool_wins(pool: list[PoolEntry]) -> list[int]:
    """Per-entry count of cells where it's (jointly) best in the pool — GEPA-style
    instance wins, the weights for stochastic parent selection. An entry that's best at
    NOTHING still gets selection weight 1 via the caller's +1, it just rarely wins."""
    means = [_cell_means(e.result) for e in pool]
    cells = set().union(*means) if means else set()
    wins = [0] * len(pool)
    for cell in cells:
        best = max(m.get(cell, float("-inf")) for m in means)
        for i, m in enumerate(means):
            if m.get(cell, float("-inf")) >= best - 1e-9:
                wins[i] += 1
    return wins


def _strictly_wins_a_cell(candidate: CandidateResult, pool: list[PoolEntry]) -> bool:
    """Pareto retention: keep a candidate only if it's STRICTLY best on at least one
    (question, subject) cell — ties don't count, or the pool fills with clones."""
    cand = _cell_means(candidate)
    pool_means = [_cell_means(e.result) for e in pool]
    for cell, mean in cand.items():
        best = max((m.get(cell, float("-inf")) for m in pool_means), default=float("-inf"))
        if mean > best + 1e-9:
            return True
    return False


async def optimize(
    questions: list[Question],
    subject_specs: list[str],
    proposer: Proposer,
    *,
    repo_dir: str,
    apply: Callable[[], None],
    auditor: Auditor | None = None,
    k: int = 3,
    rounds: int = 5,
    show: int = 12,
    steepness: float = 2.0,
    min_cells: int = 3,
    concurrency: int = 16,
    grader: str = DEFAULT_GRADER,
    subject_timeout: int = 300,
    cwd: str | None = None,
    held_out_ids: set[str] | None = None,
    save_dir: str | None = None,
    protected_paths: tuple[str, ...] = ("tests/eval/", ".github/"),
    # .git is hidden too: without it, `git show HEAD:tests/eval/cases/...` trivially
    # defeats hiding the cases. (A determined model can still find the main checkout or
    # the network — this barrier is anti-contamination, not containment; the gates and
    # auditor are the containment.)
    hide_paths: tuple[str, ...] = ("tests/eval/cases", ".git"),
    pool_size: int = 6,
    prescreen: int = 3,
    audit_retries: int = 3,
    seed: int = 1234,
    journal: Journal | None = None,
    log: Callable[[str], None] = print,
) -> OptimizeResult:
    """Run the optimization loop. ``apply`` rebuilds+restarts the harness so a fresh
    build is live; it must raise on failure. Token efficiency self-normalizes: each question
    is scored against its OWN baseline cost (median tokens of its correct baseline runs),
    frozen for the run — no budget to pick.

    ``held_out_ids`` is the anti-overfit gate: questions in this set are NEVER shown to the
    proposer (the prompt is built from train traces only), and the confidence gate is
    computed on these held-out questions. A change that just memorizes the train questions
    produces no held-out gain and is rejected. The no-correctness-regression floor still
    applies to ALL questions. With no held_out_ids the loop gates on everything (fine for a
    single-question smoke, but it CAN be gamed by encoding that question's answer).

    ``pool_size`` caps the candidate pool (mutation parents beyond the baseline);
    ``prescreen`` is how many of the parent's worst train questions get a cheap k=1 functional smoke
    canonical-phrasing check before the full measure (0 disables; it also auto-disables
    when it wouldn't be cheaper than the full suite). ``audit_retries`` is how many times
    a blocked proposal goes back to the proposer with the auditor's findings to amend
    (0 = a block is final). ``seed`` makes parent selection reproducible."""
    if not _is_clean(repo_dir):
        raise RuntimeError(
            f"{repo_dir} has uncommitted changes; the loop reverts with git and would "
            "clobber them. Commit or stash first (run on a throwaway worktree/branch)."
        )
    held_out_ids = held_out_ids or set()
    train_ids = {q.id for q in questions} - held_out_ids
    if held_out_ids:
        log(f"train questions: {sorted(train_ids)} | held-out (gate): {sorted(held_out_ids)}")
    # Fail loudly up front if the gate is unwinnable: fewer gate cells than min_cells
    # means is_confident is False forever and NOTHING can ever be committed.
    gate_cells = (len(held_out_ids) or len(questions)) * len(subject_specs)
    if gate_cells < min_cells:
        log(
            f"[bold yellow]WARNING[/bold yellow]: only {gate_cells} gate cell(s) "
            f"({len(held_out_ids) or len(questions)} gate question(s) x "
            f"{len(subject_specs)} subject(s)) but min-cells={min_cells} — the confidence "
            f"gate can NEVER pass, so no candidate will be committed. Lower --min-cells "
            f"or hold out more questions."
        )

    run_root = Path(save_dir) if save_dir else Path(tempfile.mkdtemp(prefix="harden-"))
    # The run's anchor: every candidate is a patch vs THIS commit. If something else
    # commits to the worktree mid-run (a human, another agent), patches stop applying
    # and uncommitted external edits would be misread as proposal files / reverted —
    # detect it at each round boundary and stop cleanly instead.
    start_head = _git(repo_dir, "rev-parse", "HEAD")

    async def measure(
        label: str,
        refs: dict[str, float] | None = None,
        qs: list[Question] | None = None,
        k_override: int | None = None,
    ) -> CandidateResult:
        qs = qs or questions
        kk = k_override or k
        n = sum(1 + len(q.variations) for q in qs) * len(subject_specs) * kk
        log(f"  measuring {label}: {n} runs ({len(subject_specs)} subj x k={kk}) via promptfoo...")
        return await measure_candidate(
            qs,
            subject_specs,
            k=kk,
            run_dir=str(run_root / label),
            refs=refs,
            steepness=steepness,
            grader=grader,
            concurrency=concurrency,
            subject_timeout=subject_timeout,
            cwd=cwd,
        )

    log("rebuilding harness (baseline)...")
    apply()
    baseline = await measure("baseline")
    _log_breakdown(log, "baseline", baseline)
    # Freeze the per-question token reference from the baseline; every candidate is scored
    # against it, so "more efficient" means "fewer tokens than the harness costs today".
    refs = baseline.refs
    log(f"  token reference (per question, from baseline): "
        f"{ {q: int(t) for q, t in sorted(refs.items())} }")
    baseline_traces = run_root / "baseline" / "traces"
    charts.record(
        run_root, round_n=0, label="baseline", accepted=None, reason="baseline",
        result=baseline, log=log,
    )
    result = OptimizeResult(baseline=baseline)
    pool: list[PoolEntry] = [PoolEntry("baseline", "", baseline, str(baseline_traces))]
    rng = random.Random(seed)
    # What the proposer already tried this run and how it fared — fed into every
    # subsequent prompt so it explores instead of re-proposing a rejected idea.
    history: list[str] = []

    # What the current round actually proposed, for the journal: updated at every patch
    # capture (including amendments) so _record fingerprints the judged state, then
    # cleared. ``rejected_fps`` seeds from the persistent journal and grows in-run, so
    # an exact resubmission of any rejected patch is caught without re-measuring.
    round_patch = {"text": ""}
    rejected_fps = journal.rejected_fingerprints() if journal else set()

    def _record(
        n: int, accepted: bool, reason: str, after: CandidateResult, summary: str, parent: str
    ) -> None:
        result.rounds.append(_round(n, accepted, reason, baseline, after, summary))
        charts.record(
            run_root, round_n=n, label=f"round{n}", accepted=accepted, reason=reason,
            result=after if after is not baseline else None,
            summary=summary, parent=parent, log=log,
        )
        fp = patch_fingerprint(round_patch["text"]) if round_patch["text"] else ""
        round_patch["text"] = ""
        if fp and not accepted:
            rejected_fps.add(fp)
        if journal:
            journal.append(
                run=run_root.name, round_n=n, accepted=accepted, reason=reason,
                summary=summary,
                score_before=baseline.score if after is not baseline else None,
                score_after=after.score if after is not baseline else None,
                fingerprint=fp,
            )
        outcome = (
            f"CHAMPION (score {baseline.score:.3f} -> {after.score:.3f})"
            if accepted
            else f"{reason}"
            + (
                f" (score {baseline.score:.3f} -> {after.score:.3f})"
                if after is not baseline
                else ""
            )
        )
        history.append(
            f"- round {n} (parent={parent}) {outcome}. "
            f"Proposal: {' '.join(summary.split())[:300]}"
        )

    def _gate_runs(res: CandidateResult):
        return filter_runs(res.runs, held_out_ids) if held_out_ids else res.runs

    def _gate_score(res: CandidateResult) -> float:
        runs = _gate_runs(res)
        return statistics.mean(r.score for r in runs) if runs else 0.0

    for n in range(1, rounds + 1):
        if _git(repo_dir, "rev-parse", "HEAD") != start_head:
            log(
                "[bold red]HEAD MOVED[/bold red]: the worktree was committed to from "
                "outside this run — candidate patches no longer apply to it. Stopping "
                "cleanly (pool candidates so far are in the run artifacts). Don't edit "
                "the worktree while a run is active."
            )
            break
        log(f"[bold cyan]--- round {n}/{rounds} ---[/bold cyan]")
        # Stochastic Pareto parent selection: entries that are best on more cells get
        # picked more; +1 keeps every entry (incl. a winless baseline) selectable.
        wins = _pool_wins(pool)
        parent = rng.choices(pool, weights=[1 + w for w in wins], k=1)[0]
        if len(pool) > 1:
            standing = ", ".join(f"{e.label}={w}" for e, w in zip(pool, wins))
            log(f"round {n}: pool cell-wins: {standing} -> parent [bold]{parent.label}[/bold]")
        _apply_patch(repo_dir, parent.patch)
        pre_patch = _capture_patch(repo_dir)

        # The proposer only ever sees TRAIN traces — never the held-out questions. The
        # prompt is a lean summary; the FULL traces live on disk for it to read.
        train_records = [r for r in parent.result.records if r.question.id in train_ids]
        prompt = build_proposal_prompt(
            train_records, traces_dir=parent.traces_dir, limit=show, history=history,
            prior_runs=journal.render() if journal else "",
        )
        _dump(save_dir, f"round{n}_proposal_prompt.txt", prompt)
        log(f"round {n}: proposing harness edits (this can take several minutes)...")
        # Information barrier: the eval cases (questions, rubrics, held-out split) are
        # moved out of the tree while the proposer runs — it can't read what grades it.
        with _hidden([Path(repo_dir) / p for p in hide_paths]):
            proposal = proposer.propose(prompt)
        _dump(save_dir, f"round{n}_proposal_summary.txt", proposal.summary)
        if not proposal.ok:
            log(f"round {n}: [red]proposer failed[/red]: {escape(proposal.summary[:200])}")
            _revert(repo_dir)
            _record(n, False, "proposer-failed", baseline, proposal.summary, parent.label)
            continue
        patch = _capture_patch(repo_dir)
        if patch == pre_patch:
            log(f"round {n}: proposer made no edits")
            _revert(repo_dir)
            _record(n, False, "no-edits", baseline, proposal.summary, parent.label)
            continue
        touched = _changed_paths(repo_dir)
        log(f"round {n}: proposal summary: {escape(' '.join(proposal.summary.split())[:280])}")
        round_patch["text"] = patch
        if patch_fingerprint(patch) in rejected_fps:
            log(
                f"round {n}: [bold yellow]DUPLICATE[/bold yellow] of a previously "
                "rejected proposal (journal fingerprint match) — reverting without "
                "re-judging"
            )
            _revert(repo_dir)
            _record(n, False, "duplicate-rejected", baseline, proposal.summary, parent.label)
            continue
        log(f"round {n}: edited {len(touched)} file(s): {', '.join(touched)[:300]}")

        # Deterministic guards BEFORE the LLM audit — these can't be argued past.
        violations = _protected_violations(repo_dir, protected_paths)
        if violations:
            log(
                f"round {n}: [bold red]PROTECTED PATHS[/bold red] touched "
                f"({', '.join(violations)[:200]}) — reverting"
            )
            _revert(repo_dir)
            _record(
                n, False, f"protected-paths:{','.join(violations)[:120]}",
                baseline, proposal.summary, parent.label,
            )
            continue
        leaks = _leak_check(patch, questions)
        if leaks:
            log(f"round {n}: [bold red]LEAK CHECK[/bold red] ({len(leaks)} hit(s)) — reverting")
            _dump(save_dir, f"round{n}_leaks.txt", "\n".join(leaks))
            _revert(repo_dir)
            _record(n, False, "rubric-leak", baseline, proposal.summary, parent.label)
            continue

        # Adversarial audit BEFORE the expensive build+measure: a fresh-context reviewer
        # tries to refuse the diff for answer-leakage / misplacement / eval-infra gaming —
        # the cheats the held-out gate can't see. A block isn't final: the findings go
        # back to the proposer to AMEND its still-in-tree edits (move/delete the flagged
        # content), up to ``audit_retries`` times — that salvages an expensive proposal
        # whose only sin is placement. Deterministic guards re-run on every amendment.
        if auditor is not None:
            blocked_text = ""
            for attempt in range(audit_retries + 1):
                log(f"round {n}: auditing the proposed diff (attempt {attempt + 1})...")
                # The auditor reads the repo for placement context but must not read the
                # cases/rubrics: its findings are fed back to the proposer on amend
                # retries, so anything it quotes would tunnel through the barrier. It
                # already gets every question text in its prompt — that's all it needs.
                with _hidden([Path(repo_dir) / p for p in hide_paths]):
                    verdict = auditor.audit(patch, [q.text for q in questions])
                _dump(save_dir, f"round{n}_audit{attempt + 1}.txt", verdict.text())
                log(f"round {n}: audit verdict: {escape(' '.join(verdict.summary.split())[:240])}")
                if getattr(verdict, "unavailable", False):
                    # The auditor itself failed (after its own retries): fail closed
                    # without amend rounds — no proposal change can fix auditor downtime.
                    blocked_text = verdict.text()

                    break
                if not verdict.blocked:
                    blocked_text = ""
                    if verdict.findings:
                        log(
                            f"round {n}: audit passed with {len(verdict.findings)} "
                            f"warning(s) (see artifacts)"
                        )
                    break
                blocked_text = verdict.text()
                if attempt == audit_retries:
                    break
                log(
                    f"round {n}: [bold red]AUDIT BLOCKED[/bold red] "
                    f"({len(verdict.findings)} finding(s)) — sending findings back to the "
                    f"proposer to amend ({attempt + 1}/{audit_retries})"
                )
                amend = build_amend_prompt(verdict.text(), patch)
                _dump(save_dir, f"round{n}_amend{attempt + 1}.txt", amend)
                with _hidden([Path(repo_dir) / p for p in hide_paths]):
                    amended = proposer.propose(amend)
                if not amended.ok:
                    log(f"round {n}: [red]amend failed[/red]: {escape(amended.summary[:200])}")
                    break
                new_patch = _capture_patch(repo_dir)
                if new_patch == patch:
                    log(f"round {n}: proposer made no amendments — giving up")
                    break
                patch = new_patch
                round_patch["text"] = patch
                proposal = amended
                # Re-screen the amended diff with the unfoolable guards before re-auditing.
                violations = _protected_violations(repo_dir, protected_paths)
                leaks = _leak_check(patch, questions)
                if violations or leaks:
                    blocked_text = f"amendment hit deterministic guards: {violations or leaks}"
                    break
            if blocked_text:
                log(f"round {n}: [bold red]AUDIT BLOCKED[/bold red] (final) — reverting")
                _revert(repo_dir)
                _record(n, False, "audit-blocked", baseline, blocked_text, parent.label)
                continue

        _dump(save_dir, f"round{n}.patch", patch)  # re-appliable: git apply round{n}.patch
        log(f"round {n}: proposal made edits; rebuilding + restarting harness...")
        try:
            apply()
        except Exception as exc:  # noqa: BLE001 - a broken build is a rejected proposal
            log(f"round {n}: [red]build failed[/red], reverting: {type(exc).__name__}: {escape(str(exc))}")
            _revert(repo_dir)
            _record(n, False, "build-failed", baseline, proposal.summary, parent.label)
            continue

        # Cheap prescreen: a FUNCTIONAL smoke on the parent's worst train questions
        # (canonical phrasing, k=1). Its only job is catching a candidate that broke
        # the harness at runtime — zero correct runs where the parent had working
        # ones — before paying for the full measure. It deliberately does NOT
        # compare scores: a handful of k=1 cells cannot statistically separate a
        # regression from noise (a score-based prescreen was observed killing
        # candidates on variance), and the full measure's gates own that decision.
        if 0 < prescreen < len(questions):
            by_q: dict[str, list[float]] = {}
            parent_correct: set[str] = set()
            for r in parent.result.runs:
                if r.question_id in train_ids:
                    by_q.setdefault(r.question_id, []).append(r.score)
                    if r.correct:
                        parent_correct.add(r.question_id)
            worst_qids = sorted(by_q, key=lambda q: statistics.mean(by_q[q]))[:prescreen]
            sub_qs = [replace(q, variations=[]) for q in questions if q.id in worst_qids]
            pre = await measure(f"round{n}_prescreen", refs=refs, qs=sub_qs, k_override=1)
            parent_functional = any(q in parent_correct for q in worst_qids)
            correct_runs = sum(1 for r in pre.runs if r.correct)
            if pre.runs and parent_functional and correct_runs == 0:
                log(
                    f"round {n}: [bold yellow]PRESCREEN[/bold yellow] 0/{len(pre.runs)} "
                    f"correct runs on {worst_qids} where the parent was functional — "
                    f"candidate looks broken; rejecting without the full measure"
                )
                _revert(repo_dir)
                _record(n, False, "prescreen", baseline, proposal.summary, parent.label)
                continue
            log(f"round {n}: prescreen ok ({correct_runs}/{len(pre.runs)} runs correct)")

        candidate = await measure(f"round{n}_candidate", refs=refs)
        _log_breakdown(log, f"round{n} candidate", candidate)
        # Gates are ALWAYS vs the ORIGINAL baseline (the state HEAD points at): correctness
        # must not regress anywhere, and the champion bar is confident improvement on the
        # held-out questions the proposer never saw.
        gate_label = "held-out" if held_out_ids else "all"
        regressed = not no_correctness_regression(baseline.runs, candidate.runs)
        confident = is_confident(
            _gate_runs(baseline), _gate_runs(candidate), min_cells=min_cells, log=log
        )
        n_cells = len({(r.question_id, r.subject) for r in _gate_runs(candidate)})
        log(
            f"round {n}: gate on {gate_label} ({n_cells} cells) — "
            f"regressed={regressed} confident={confident}"
        )
        _revert(repo_dir)  # the candidate lives on as a patch; the tree goes back to HEAD

        if regressed:
            log(
                f"round {n}: [bold yellow]REJECT[/bold yellow] (regressed-correctness) score "
                f"{baseline.score:.3f} -> {candidate.score:.3f} "
                f"pass {baseline.pass_rate:.2f} -> {candidate.pass_rate:.2f}"
            )
            _record(n, False, "regressed-correctness", candidate, proposal.summary, parent.label)
            continue

        entry = PoolEntry(
            label=f"round {n}",
            patch=patch,
            result=candidate,
            traces_dir=str(run_root / f"round{n}_candidate" / "traces"),
            champion=confident,
        )
        if confident:
            log(
                f"round {n}: [bold green]CHAMPION[/bold green] score {baseline.score:.3f} -> "
                f"{candidate.score:.3f} pass {baseline.pass_rate:.2f} -> {candidate.pass_rate:.2f}"
            )
            pool.append(entry)
            _record(n, True, "accepted", candidate, proposal.summary, parent.label)
        elif _strictly_wins_a_cell(candidate, pool):
            log(
                f"round {n}: [yellow]POOL[/yellow] (not confident overall, but best on >=1 "
                f"cell) score {baseline.score:.3f} -> {candidate.score:.3f}"
            )
            pool.append(entry)
            _record(n, False, "pool-added", candidate, proposal.summary, parent.label)
        else:
            log(
                f"round {n}: [bold yellow]REJECT[/bold yellow] (not-confident) score "
                f"{baseline.score:.3f} -> {candidate.score:.3f} "
                f"pass {baseline.pass_rate:.2f} -> {candidate.pass_rate:.2f}"
            )
            _record(n, False, "not-confident", candidate, proposal.summary, parent.label)
            continue
        # Cap the pool: drop the winless tail (never the baseline, never a champion —
        # champions are commit candidates, not just mutation parents).
        while len(pool) > pool_size:
            wins = _pool_wins(pool)
            droppable = [
                i for i, e in enumerate(pool) if e.label != "baseline" and not e.champion
            ]
            if not droppable:
                break
            drop = min(droppable, key=lambda i: wins[i])
            log(f"  pool full: dropping {pool[drop].label} ({wins[drop]} cell-wins)")
            pool.pop(drop)

    # One commit at the end: the best champion's whole patch. HEAD never moved during the
    # run, so every pool patch applies cleanly; non-champion pool entries were mutation
    # fuel, not commits.
    champions = [e for e in pool if e.champion]
    if champions:
        best = max(champions, key=lambda e: _gate_score(e.result))
        _apply_patch(repo_dir, best.patch)
        _commit(repo_dir, f"harden {best.label}: {baseline.score:.3f} -> {best.result.score:.3f}")
        log(
            f"[bold green]COMMITTED[/bold green] {best.label}: score {baseline.score:.3f} -> "
            f"{best.result.score:.3f} (best of {len(champions)} champion(s))"
        )
    elif len(pool) > 1:
        best = max(pool[1:], key=lambda e: _gate_score(e.result))
        log(
            f"[bold yellow]NO COMMIT[/bold yellow]: no candidate cleared the confidence bar "
            f"on the {'held-out' if held_out_ids else 'full'} questions. Best was "
            f"[bold]{best.label}[/bold] (score {baseline.score:.3f} -> {best.result.score:.3f}); "
            f"its patch + traces are in the run artifacts."
        )
    else:
        log("[bold yellow]NO COMMIT[/bold yellow]: no proposal survived the gates this run.")
    apply()  # leave the live harness matching the final tree
    log(f"done: {result.accepted}/{rounds} champion round(s), {len(pool) - 1} pool candidate(s)")
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
