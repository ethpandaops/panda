"""Integration test for harden.loop — accept/commit + reject/revert + the gates + auditor,
on a throwaway git repo. Measurement (promptfoo, a subprocess) is mocked: a stub
``measure_candidate`` returns a CandidateResult driven by a shared ``improved`` flag that
the stub ``apply`` flips, so we exercise the loop's control flow with no LLM/network/build.
"""

from __future__ import annotations

import statistics
import subprocess
from pathlib import Path

import pytest

from harden import loop as loop_mod
from harden.auditor import AuditVerdict
from harden.loop import optimize
from harden.proposer import ProposalResult
from harden.runner import CandidateResult, Question, RunRecord
from harden.scoring import candidate_score, pass_rate, score_run
from harden.trace import RunTrace, ToolCall


def _git(repo, *args):
    return subprocess.run(
        ["git", "-C", str(repo), *args], text=True, capture_output=True, check=True
    ).stdout


@pytest.fixture
def repo(tmp_path):
    _git(tmp_path, "init", "-q")
    _git(tmp_path, "config", "user.email", "t@t.t")
    _git(tmp_path, "config", "user.name", "t")
    (tmp_path / "src.txt").write_text("baseline\n")
    _git(tmp_path, "add", "-A")
    _git(tmp_path, "commit", "-qm", "init")
    return tmp_path


def _stub_measure(state, *, subjects=("s",), correct_when_improved=True, improves=None):
    """Build a fake measure_candidate: when ``improved`` (and the question is in
    ``improves``), runs are lean (high score); otherwise wasteful. Always correct unless
    ``correct_when_improved`` is False."""

    async def _measure(questions, subject_specs, *, k, run_dir, refs=None, steepness=2.0, **_):
        specs = subject_specs or list(subjects)
        # Per-question plan (the stub is uniform within a measure: lean-or-wasteful).
        plan = {}
        for q in questions:
            good = state.get("improved") and (improves is None or q.id in improves)
            plan[q.id] = (4000 if good else 40000, correct_when_improved if good else True)
        # Self-normalize from this measure's correct runs unless the loop froze refs (baseline).
        used = refs or {qid: float(t) for qid, (t, c) in plan.items() if c} or {
            qid: float(t) for qid, (t, _c) in plan.items()
        }
        runs, records, by_subject = [], [], {}
        for q in questions:
            tokens, correct = plan[q.id]
            for subj in specs:
                for _ in range(k):
                    trace = RunTrace(
                        question=q.text,
                        subject=subj,
                        output="answer" if correct else "",
                        tool_calls=[ToolCall("bash", "cmd", "out")],
                        input_tokens=tokens,
                        output_tokens=0,
                    )
                    rs = score_run(
                        trace,
                        correct=correct,
                        correctness=1.0 if correct else 0.0,
                        ref=used.get(q.id, 0.0),
                        steepness=steepness,
                        question_id=q.id,
                    )
                    runs.append(rs)
                    records.append(RunRecord(question=q, trace=trace, score=rs))
                    by_subject.setdefault(subj, []).append(rs.score)
        return CandidateResult(
            runs=runs,
            records=records,
            score=candidate_score(runs),
            pass_rate=pass_rate(runs),
            by_subject={s: statistics.mean(v) for s, v in by_subject.items() if v},
            refs=used,
        )

    return _measure


class _StubProposer:
    """Writes a file so the tree goes dirty (a 'proposal'). apply() reads it."""

    def __init__(self, repo, state):
        self.repo = repo
        self.state = state

    def propose(self, prompt: str) -> ProposalResult:
        (Path(self.repo) / "proposal.txt").write_text("edit\n")
        return ProposalResult(ok=True, summary="wrote proposal.txt")


class _StubAuditor:
    def __init__(self, blocked):
        self.blocked = blocked

    def audit(self, diff, questions) -> AuditVerdict:
        f = [{"severity": "block", "kind": "answer_leakage", "file": "x", "issue": "y"}]
        return AuditVerdict(blocked=self.blocked, summary="stub", findings=f if self.blocked else [])


def _apply_factory(repo, state):
    def apply():
        state["improved"] = (Path(repo) / "proposal.txt").exists()

    return apply


_QS = [Question(id=f"q{i}", text=f"question {i}") for i in range(3)]


@pytest.mark.asyncio
async def test_accepts_and_commits_improvement(repo, monkeypatch):
    state = {"improved": False}
    monkeypatch.setattr(loop_mod, "measure_candidate", _stub_measure(state))
    result = await optimize(
        _QS, ["s"], _StubProposer(repo, state),
        repo_dir=str(repo), apply=_apply_factory(repo, state),
        k=2, rounds=1, log=lambda *_: None,
    )
    assert result.accepted == 1
    assert result.rounds[0].reason == "accepted"
    assert _git(repo, "status", "--porcelain").strip() == ""
    assert (repo / "proposal.txt").exists()
    assert "harden round 1" in _git(repo, "log", "--oneline")


@pytest.mark.asyncio
async def test_rejects_and_reverts_regression(repo, monkeypatch):
    state = {"improved": False}
    monkeypatch.setattr(loop_mod, "measure_candidate", _stub_measure(state, correct_when_improved=False))
    # k=3 is the regression gate's power floor: a 1-phrasing cell collapsing k/k -> 0/k
    # only reaches significance (Fisher one-sided p=0.05) from 3 runs up — at k=2 even a
    # total collapse is indistinguishable from judge noise (p=0.167).
    result = await optimize(
        _QS, ["s"], _StubProposer(repo, state),
        repo_dir=str(repo), apply=_apply_factory(repo, state),
        k=3, rounds=1, log=lambda *_: None,
    )
    assert result.accepted == 0
    assert result.rounds[0].reason == "regressed-correctness"
    assert _git(repo, "status", "--porcelain").strip() == ""
    assert not (repo / "proposal.txt").exists()
    assert "harden round" not in _git(repo, "log", "--oneline")


@pytest.mark.asyncio
async def test_refuses_dirty_tree(repo, monkeypatch):
    monkeypatch.setattr(loop_mod, "measure_candidate", _stub_measure({"improved": False}))
    (repo / "src.txt").write_text("uncommitted change\n")
    with pytest.raises(RuntimeError, match="uncommitted changes"):
        await optimize(
            _QS, ["s"], _StubProposer(repo, {}),
            repo_dir=str(repo), apply=lambda: None, k=1, rounds=1,
            log=lambda *_: None,
        )


_SPLIT = [Question(id=q, text=q) for q in ("q0", "q1", "q2")]


@pytest.mark.asyncio
async def test_held_out_rejects_train_only_improvement(repo, monkeypatch):
    state = {"improved": False}
    monkeypatch.setattr(loop_mod, "measure_candidate", _stub_measure(state, improves={"q0", "q1"}))
    result = await optimize(
        _SPLIT, ["s"], _StubProposer(repo, state),
        repo_dir=str(repo), apply=_apply_factory(repo, state),
        k=2, rounds=1, min_cells=1, held_out_ids={"q2"}, log=lambda *_: None,
    )
    # Not committed (no held-out gain), but Pareto-retained as a mutation parent: it IS
    # strictly best on the train cells, and a future round may build on it.
    assert result.accepted == 0
    assert result.rounds[0].reason == "pool-added"
    assert not (repo / "proposal.txt").exists()
    assert "harden round" not in _git(repo, "log", "--oneline")


@pytest.mark.asyncio
async def test_held_out_accepts_generalizing_improvement(repo, monkeypatch):
    state = {"improved": False}
    monkeypatch.setattr(loop_mod, "measure_candidate", _stub_measure(state, improves={"q0", "q1", "q2"}))
    result = await optimize(
        _SPLIT, ["s"], _StubProposer(repo, state),
        repo_dir=str(repo), apply=_apply_factory(repo, state),
        k=2, rounds=1, min_cells=1, held_out_ids={"q2"}, log=lambda *_: None,
    )
    assert result.accepted == 1
    assert result.rounds[0].reason == "accepted"


@pytest.mark.asyncio
async def test_auditor_blocks_a_would_be_accept(repo, monkeypatch):
    state = {"improved": False}
    monkeypatch.setattr(loop_mod, "measure_candidate", _stub_measure(state))
    result = await optimize(
        _QS, ["s"], _StubProposer(repo, state),
        repo_dir=str(repo), apply=_apply_factory(repo, state),
        auditor=_StubAuditor(blocked=True), k=2, rounds=1, log=lambda *_: None,
    )
    assert result.accepted == 0
    assert result.rounds[0].reason == "audit-blocked"
    assert not (repo / "proposal.txt").exists()
    assert "harden round" not in _git(repo, "log", "--oneline")


@pytest.mark.asyncio
async def test_audit_block_is_amended_then_accepted(repo, monkeypatch):
    # First audit blocks; the findings go back to the proposer, whose amendment (a
    # changed diff) passes the re-audit -> the round proceeds to measure and wins.
    state = {"improved": False}
    monkeypatch.setattr(loop_mod, "measure_candidate", _stub_measure(state))

    class AmendingProposer(_StubProposer):
        calls = 0

        def propose(self, prompt):
            AmendingProposer.calls += 1
            (Path(self.repo) / "proposal.txt").write_text(f"edit v{AmendingProposer.calls}\n")
            return ProposalResult(ok=True, summary=f"attempt {AmendingProposer.calls}")

    class OnceBlockingAuditor:
        calls = 0

        def audit(self, diff, questions):
            OnceBlockingAuditor.calls += 1
            blocked = OnceBlockingAuditor.calls == 1
            f = [{"severity": "block", "kind": "misplacement", "file": "x", "issue": "move it"}]
            return AuditVerdict(blocked, "stub", f if blocked else [])

    result = await optimize(
        _QS, ["s"], AmendingProposer(repo, state),
        repo_dir=str(repo), apply=_apply_factory(repo, state),
        auditor=OnceBlockingAuditor(), k=2, rounds=1, log=lambda *_: None,
    )
    assert AmendingProposer.calls == 2  # initial proposal + one amendment
    assert OnceBlockingAuditor.calls == 2  # block, then pass
    assert result.accepted == 1
    assert "harden round 1" in _git(repo, "log", "--oneline")


@pytest.mark.asyncio
async def test_clean_audit_does_not_block_accept(repo, monkeypatch):
    state = {"improved": False}
    monkeypatch.setattr(loop_mod, "measure_candidate", _stub_measure(state))
    result = await optimize(
        _QS, ["s"], _StubProposer(repo, state),
        repo_dir=str(repo), apply=_apply_factory(repo, state),
        auditor=_StubAuditor(blocked=False), k=2, rounds=1, log=lambda *_: None,
    )
    assert result.accepted == 1


@pytest.mark.asyncio
async def test_prescreen_rejects_cheaply_before_full_measure(repo, monkeypatch):
    # A candidate that's clearly worse on the parent's worst questions is killed by the
    # k=1 prescreen: only baseline + prescreen measures run, never the full suite.
    state = {"improved": False}
    calls: list[str] = []
    inner = _stub_measure(state, correct_when_improved=False)

    async def counting(questions, subject_specs, *, run_dir, **kw):
        calls.append(Path(run_dir).name)
        return await inner(questions, subject_specs, run_dir=run_dir, **kw)

    monkeypatch.setattr(loop_mod, "measure_candidate", counting)
    qs = [Question(id=f"q{i}", text=f"question {i}") for i in range(4)]  # prescreen(3) < 4
    result = await optimize(
        qs, ["s"], _StubProposer(repo, state),
        repo_dir=str(repo), apply=_apply_factory(repo, state),
        k=3, rounds=1, log=lambda *_: None,
    )
    assert result.rounds[0].reason == "prescreen"
    assert calls == ["baseline", "round1_prescreen"]
    assert not (repo / "proposal.txt").exists()


def test_question_prompts_single_and_multi_turn():
    assert Question(id="a", text="one").prompts == ["one"]
    assert Question(id="b", text="one", followups=["two", "three"]).prompts == ["one", "two", "three"]
