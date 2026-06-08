"""Integration test for harden.loop — the accept/commit + reject/revert + clean-tree
guard, exercised end to end on a throwaway git repo with stub subject/judge/proposer.

No LLM, no network, no panda build: the stubs make a "proposal" flip a shared flag via
``apply`` so we can assert the loop commits an improvement and git-reverts a regression.
"""

from __future__ import annotations

import subprocess
from pathlib import Path

import pytest

from harden.auditor import AuditVerdict
from harden.judge import Verdict
from harden.loop import optimize
from harden.proposer import ProposalResult
from harden.runner import Question
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


class _StubSubject:
    """Returns a good or bad trace depending on a shared mutable state flag."""

    def __init__(self, state):
        self.name = "stub:m:cli"
        self.state = state

    async def run(self, question: str) -> RunTrace:
        improved = self.state["improved"]
        tokens = 5000 if improved else 40000
        correct = self.state.get("correct_when_improved", True) if improved else True
        return RunTrace(
            question=question,
            subject=self.name,
            output="answer" if correct else "",
            tool_calls=[ToolCall("bash", "cmd", "out")],
            input_tokens=tokens,
            output_tokens=0,
            crashed=False,
        )


class _StubJudge:
    async def judge(self, trace: RunTrace) -> Verdict:
        ok = bool(trace.output)
        return Verdict(correct=ok, correctness=1.0 if ok else 0.0, reason="stub")


class _StubProposer:
    """Writes a file so the tree goes dirty (a 'proposal'). apply() reads it."""

    def __init__(self, repo, state):
        self.repo = repo
        self.state = state

    def propose(self, prompt: str) -> ProposalResult:
        (Path(self.repo) / "proposal.txt").write_text("edit\n")
        return ProposalResult(ok=True, summary="wrote proposal.txt")


def _apply_factory(repo, state):
    def apply():
        # a "built" proposal is live iff the proposed file is present
        state["improved"] = (Path(repo) / "proposal.txt").exists()

    return apply


_QS = [Question(id=f"q{i}", text=f"question {i}") for i in range(3)]


@pytest.mark.asyncio
async def test_accepts_and_commits_improvement(repo):
    state = {"improved": False}
    subject = _StubSubject(state)
    result = await optimize(
        _QS,
        [subject],
        _StubJudge(),
        _StubProposer(repo, state),
        repo_dir=str(repo),
        apply=_apply_factory(repo, state),
        budget=10000,
        k=2,
        rounds=1,
        log=lambda *_: None,
    )
    assert result.accepted == 1
    assert result.rounds[0].reason == "accepted"
    # change was committed, tree is clean, the proposed file persists
    assert _git(repo, "status", "--porcelain").strip() == ""
    assert (repo / "proposal.txt").exists()
    assert "harden round 1" in _git(repo, "log", "--oneline")


@pytest.mark.asyncio
async def test_rejects_and_reverts_regression(repo):
    # proposal makes runs WRONG when "improved" -> correctness regresses -> reject + revert
    state = {"improved": False, "correct_when_improved": False}
    subject = _StubSubject(state)
    result = await optimize(
        _QS,
        [subject],
        _StubJudge(),
        _StubProposer(repo, state),
        repo_dir=str(repo),
        apply=_apply_factory(repo, state),
        budget=10000,
        k=2,
        rounds=1,
        log=lambda *_: None,
    )
    assert result.accepted == 0
    assert result.rounds[0].reason == "regressed-correctness"
    # the proposed file was reverted away, tree clean, no harden commit
    assert _git(repo, "status", "--porcelain").strip() == ""
    assert not (repo / "proposal.txt").exists()
    assert "harden round" not in _git(repo, "log", "--oneline")


@pytest.mark.asyncio
async def test_refuses_dirty_tree(repo):
    (repo / "src.txt").write_text("uncommitted change\n")
    with pytest.raises(RuntimeError, match="uncommitted changes"):
        await optimize(
            _QS,
            [_StubSubject({"improved": False})],
            _StubJudge(),
            _StubProposer(repo, {}),
            repo_dir=str(repo),
            apply=lambda: None,
            budget=10000,
            k=1,
            rounds=1,
            log=lambda *_: None,
        )


class _SelectiveSubject:
    """Improves only the questions in ``improves`` once a proposal is applied — used to
    simulate a change that helps the train questions but not the held-out one."""

    def __init__(self, state, improves: set[str]):
        self.name = "stub:m:cli"
        self.state = state
        self.improves = improves

    async def run(self, question: str) -> RunTrace:
        good = self.state["improved"] and question in self.improves
        return RunTrace(
            question=question,
            subject=self.name,
            output="answer",  # always correct, so the gate hinges on efficiency only
            tool_calls=[ToolCall("bash", "cmd", "out")],
            input_tokens=4000 if good else 40000,
            output_tokens=0,
        )


# questions whose text == id so the stub can key on the text it receives
_SPLIT_QS = [Question(id=q, text=q) for q in ("q0", "q1", "q2")]


@pytest.mark.asyncio
async def test_held_out_rejects_train_only_improvement(repo):
    # proposal improves only the TRAIN questions (q0, q1), not the held-out q2 -> reject
    state = {"improved": False}
    result = await optimize(
        _SPLIT_QS,
        [_SelectiveSubject(state, improves={"q0", "q1"})],
        _StubJudge(),
        _StubProposer(repo, state),
        repo_dir=str(repo),
        apply=_apply_factory(repo, state),
        budget=8000,
        k=2,
        rounds=1,
        min_cells=1,
        held_out_ids={"q2"},
        log=lambda *_: None,
    )
    assert result.accepted == 0
    assert result.rounds[0].reason == "not-confident"
    assert not (repo / "proposal.txt").exists()  # reverted


@pytest.mark.asyncio
async def test_held_out_accepts_generalizing_improvement(repo):
    # proposal improves ALL questions incl. the held-out q2 -> accept
    state = {"improved": False}
    result = await optimize(
        _SPLIT_QS,
        [_SelectiveSubject(state, improves={"q0", "q1", "q2"})],
        _StubJudge(),
        _StubProposer(repo, state),
        repo_dir=str(repo),
        apply=_apply_factory(repo, state),
        budget=8000,
        k=2,
        rounds=1,
        min_cells=1,
        held_out_ids={"q2"},
        log=lambda *_: None,
    )
    assert result.accepted == 1
    assert result.rounds[0].reason == "accepted"


class _StubAuditor:
    def __init__(self, blocked):
        self.blocked = blocked

    def audit(self, diff, questions) -> AuditVerdict:
        findings = [{"severity": "block", "kind": "answer_leakage", "file": "x", "issue": "y"}]
        return AuditVerdict(
            blocked=self.blocked, summary="stub", findings=findings if self.blocked else []
        )


@pytest.mark.asyncio
async def test_auditor_blocks_a_would_be_accept(repo):
    # a proposal that WOULD pass measurement is blocked by the auditor first -> reject, no commit
    state = {"improved": False}
    result = await optimize(
        _QS,
        [_StubSubject(state)],
        _StubJudge(),
        _StubProposer(repo, state),
        repo_dir=str(repo),
        apply=_apply_factory(repo, state),
        auditor=_StubAuditor(blocked=True),
        budget=10000,
        k=2,
        rounds=1,
        log=lambda *_: None,
    )
    assert result.accepted == 0
    assert result.rounds[0].reason == "audit-blocked"
    assert not (repo / "proposal.txt").exists()  # reverted
    assert "harden round" not in _git(repo, "log", "--oneline")


@pytest.mark.asyncio
async def test_clean_audit_does_not_block_accept(repo):
    state = {"improved": False}
    result = await optimize(
        _QS,
        [_StubSubject(state)],
        _StubJudge(),
        _StubProposer(repo, state),
        repo_dir=str(repo),
        apply=_apply_factory(repo, state),
        auditor=_StubAuditor(blocked=False),
        budget=10000,
        k=2,
        rounds=1,
        log=lambda *_: None,
    )
    assert result.accepted == 1
