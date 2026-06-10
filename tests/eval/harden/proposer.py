"""Proposers — the coding agent that edits the panda harness.

A ``Proposer`` reads a prompt (raw agent traces + the objective) and edits the panda
source IN PLACE in ``repo_dir``. It is the mirror of ``Subject``: the only place that
knows about a specific coding-agent CLI. Codex is implemented today; swapping in another
(Claude Code, etc.) is one more ``Proposer``. The loop never reaches into a proposer's
internals — it just calls ``propose()`` and then measures the result.
"""

from __future__ import annotations

from collections.abc import Callable
from dataclasses import dataclass
from typing import Protocol, runtime_checkable

from harden.codex import assistant_prose, run_codex


@dataclass
class ProposalResult:
    ok: bool
    summary: str  # the proposer's final message (or the error)


@runtime_checkable
class Proposer(Protocol):
    """Edits the panda source in place given a prompt. Implement for a new coding CLI."""

    def propose(self, prompt: str) -> ProposalResult: ...


class CodexProposer:
    """Runs ``codex exec`` headless against the panda repo, letting it edit files.

    Defaults to GPT-5.5 at xhigh reasoning. Runs with approvals/sandbox bypassed because
    the loop is the sandbox: every edit is gated by re-measurement + git revert, so a bad
    proposal is reverted, not trusted. Point this at a throwaway worktree, not a checkout
    you care about.
    """

    def __init__(
        self,
        repo_dir: str,
        *,
        model: str = "gpt-5.5",
        reasoning_effort: str = "xhigh",
        timeout: float = 1800.0,
        log: Callable[[str], None] | None = None,
    ) -> None:
        self.repo_dir = repo_dir
        self.model = model
        self.reasoning_effort = reasoning_effort
        self.timeout = timeout
        self.log = log

    def propose(self, prompt: str) -> ProposalResult:
        cmd = [
            "codex",
            "exec",
            "-m",
            self.model,
            "-c",
            f"model_reasoning_effort={self.reasoning_effort}",
            "-C",
            self.repo_dir,
            "--dangerously-bypass-approvals-and-sandbox",
            "-",  # read the prompt from stdin
        ]
        # Stream a FILTERED view (one line per command + codex's messages, not the tool-output
        # firehose) so the multi-minute proposal is visible without exploding the terminal.
        code, out = run_codex(cmd, prompt, timeout=self.timeout, log=self.log)
        out = out.strip()
        if code == -1:
            return ProposalResult(ok=False, summary=f"codex timed out after {self.timeout:.0f}s")
        if code != 0:
            return ProposalResult(ok=False, summary=f"codex exited {code}: {out[-1000:]}")
        # The raw tail is usually the end of a printed diff; the summary the
        # history/journal keeps must be the assistant's prose.
        return ProposalResult(ok=True, summary=(assistant_prose(out) or out)[-2000:])
