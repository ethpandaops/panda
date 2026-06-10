"""The audit gate fails CLOSED: an unavailable auditor blocks (and reverts) the
proposal instead of waving an unaudited diff through to commit."""

from __future__ import annotations

import json

import pytest

from harden import auditor as auditor_mod
from harden.auditor import AuditVerdict, CodexAuditor


def _auditor(**kwargs) -> CodexAuditor:
    kwargs.setdefault("attempts", 3)
    kwargs.setdefault("retry_delay", 0.0)
    return CodexAuditor("/tmp", **kwargs)


def test_persistent_codex_failure_fails_closed(monkeypatch: pytest.MonkeyPatch) -> None:
    calls = []

    def fake_run_codex(cmd, prompt, *, timeout, log=None):
        calls.append(cmd)
        return (1, "stream error: usage limit reached")

    monkeypatch.setattr(auditor_mod, "run_codex", fake_run_codex)

    verdict = _auditor().audit("diff --git a/x b/x", ["q"])

    assert len(calls) == 3, "auditor should retry transient failures"
    assert verdict.blocked, "an unavailable auditor must block, not fail open"
    assert verdict.unavailable
    assert "usage limit" in verdict.summary


def test_transient_failure_recovers_on_retry(monkeypatch: pytest.MonkeyPatch, tmp_path) -> None:
    attempts = {"n": 0}

    def fake_run_codex(cmd, prompt, *, timeout, log=None):
        attempts["n"] += 1
        if attempts["n"] == 1:
            return (1, "429 slow down")

        out_path = cmd[cmd.index("-o") + 1]
        with open(out_path, "w") as fh:
            json.dump({"summary": "clean", "findings": []}, fh)

        return (0, "ok")

    monkeypatch.setattr(auditor_mod, "run_codex", fake_run_codex)

    verdict = _auditor().audit("diff --git a/x b/x", ["q"])

    assert attempts["n"] == 2
    assert not verdict.blocked
    assert not verdict.unavailable


def test_block_findings_still_block(monkeypatch: pytest.MonkeyPatch) -> None:
    def fake_run_codex(cmd, prompt, *, timeout, log=None):
        out_path = cmd[cmd.index("-o") + 1]
        with open(out_path, "w") as fh:
            json.dump(
                {
                    "summary": "leak",
                    "findings": [
                        {"kind": "answer_leakage", "severity": "block", "file": "a", "issue": "x"}
                    ],
                },
                fh,
            )

        return (0, "ok")

    monkeypatch.setattr(auditor_mod, "run_codex", fake_run_codex)

    verdict = _auditor().audit("diff --git a/x b/x", ["q"])

    assert verdict.blocked
    assert not verdict.unavailable, "a real block is amendable; unavailability is not"


def test_unavailable_verdict_shape() -> None:
    v = AuditVerdict(blocked=True, summary="s", unavailable=True)

    assert "blocked=True" in v.text()
