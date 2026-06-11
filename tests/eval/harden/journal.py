"""Cross-run proposer memory — the autoresearch journal.

Within a run, the loop's ``history`` list keeps the proposer from re-proposing its own
rejects. Across runs it was amnesiac: a fresh run would happily re-submit the very patch
a previous run's gates killed. The journal fixes both ends:

- every round's outcome (verdict, scores, summary, and a content fingerprint of the
  proposed patch) is appended to one JSONL file under HARDEN_HOME, surviving runs,
  worktrees, and ``git clean``;
- ``render()`` produces a PRIOR RUNS section for the proposal prompt, so a new run's
  proposer starts knowing what already won, what was rejected, and why;
- ``rejected_fingerprints()`` lets the loop hard-reject an exact resubmission of a
  previously rejected patch before paying for an audit or a measure.

The fingerprint hashes only the patch's content lines (+/- and file headers), not hunk
offsets or blob hashes, so the same logical fix re-proposed against a drifted parent
still matches. It is deliberately exact-match: near-duplicates are the prompt's job to
discourage, not a fuzzy matcher's to guess at.

Backfill from an existing run dir's history.jsonl (charts module format):

    uv run python -m harden.journal backfill <run_dir> --cases "all files"
"""

from __future__ import annotations

import argparse
import hashlib
import json
from datetime import datetime, timezone
from pathlib import Path

DEFAULT_NAME = "journal.jsonl"


def patch_fingerprint(patch: str) -> str:
    """Content hash of a diff: file headers and added/removed lines only. Hunk headers
    (line offsets) and index lines (blob hashes) are excluded so the same logical change
    fingerprints identically even when its surroundings moved."""
    kept = [
        line
        for line in patch.splitlines()
        if line.startswith(("+", "-")) and not line.startswith(("+++", "---"))
        or line.startswith(("+++ ", "--- "))
    ]
    return hashlib.sha256("\n".join(kept).encode()).hexdigest()


class Journal:
    """Append-only JSONL of proposal outcomes, shared by every run on this machine."""

    def __init__(self, path: str | Path, context: str = ""):
        self.path = Path(path)
        self.context = context  # e.g. the cases file — stamped onto every entry

    def append(
        self,
        *,
        run: str,
        round_n: int,
        accepted: bool | None,
        reason: str,
        summary: str = "",
        score_before: float | None = None,
        score_after: float | None = None,
        fingerprint: str = "",
    ) -> None:
        entry = {
            "ts": datetime.now(timezone.utc).isoformat(timespec="seconds"),
            "run": run,
            "cases": self.context,
            "round": round_n,
            "accepted": accepted,
            "reason": reason,
            "summary": " ".join(summary.split())[:300],
        }
        if score_before is not None:
            entry["score_before"] = round(score_before, 4)
        if score_after is not None:
            entry["score_after"] = round(score_after, 4)
        if fingerprint:
            entry["fingerprint"] = fingerprint
        self.path.parent.mkdir(parents=True, exist_ok=True)
        with open(self.path, "a") as f:
            f.write(json.dumps(entry) + "\n")

    def entries(self) -> list[dict]:
        if not self.path.exists():
            return []
        return [json.loads(line) for line in self.path.read_text().splitlines() if line.strip()]

    def rejected_fingerprints(self) -> set[str]:
        return {
            e["fingerprint"]
            for e in self.entries()
            if e.get("fingerprint") and e.get("accepted") is False
        }

    def render(self, max_entries: int = 25, max_chars: int = 6000) -> str:
        """The PRIOR RUNS prompt section: most recent entries first, capped. Returns ""
        when there is nothing to tell."""
        entries = self.entries()
        if not entries:
            return ""
        lines = []
        for e in reversed(entries[-max_entries:]):
            verdict = (
                "CHAMPION"
                if e.get("accepted")
                else e.get("reason", "rejected")
                if e.get("accepted") is False
                else e.get("reason", "")
            )
            scores = ""
            if "score_before" in e and "score_after" in e:
                scores = f" {e['score_before']:.3f}->{e['score_after']:.3f}"
            summary = e.get("summary", "")
            lines.append(
                f"- [{e.get('run', '?')} r{e.get('round', '?')}, {e.get('cases', '?')}] "
                f"{verdict}{scores}: {summary}"
            )
        text = "\n".join(lines)
        if len(text) > max_chars:
            text = text[:max_chars] + "\n… [older entries truncated]"
        return (
            "PRIOR OPTIMIZATION RUNS (persistent journal, most recent first) — what "
            "earlier runs against this harness tried and how it ended. CHAMPION entries "
            "are ALREADY MERGED into the code you see: do not re-propose them. Rejected "
            "entries failed the gates for the stated reason: do not re-submit the same "
            "approach — reason about WHY it failed and explore a different angle:\n"
            + text
        )


def backfill(run_dir: str | Path, journal: Journal) -> int:
    """Import a finished run's per-round outcomes from its history.jsonl (written by
    harden.charts). Patch fingerprints are recovered from roundN.patch files when
    present. Returns the number of entries imported."""
    run_dir = Path(run_dir)
    history_path = run_dir / "history.jsonl"
    if not history_path.exists():
        raise FileNotFoundError(f"{history_path} not found — run harden.charts first?")
    count = 0
    for line in history_path.read_text().splitlines():
        if not line.strip():
            continue
        h = json.loads(line)
        if h.get("round", 0) == 0:
            continue  # the baseline is not a proposal
        patch_path = run_dir / f"round{h['round']}.patch"
        fp = patch_fingerprint(patch_path.read_text()) if patch_path.exists() else ""
        journal.append(
            run=run_dir.name,
            round_n=h["round"],
            accepted=h.get("accepted"),
            reason=h.get("reason", ""),
            summary=h.get("summary", ""),
            score_before=None,
            score_after=h.get("score"),
            fingerprint=fp,
        )
        count += 1
    return count


def main() -> None:
    # Late import: harden/ must not depend on scripts/ at module level (scripts imports
    # harden); the CLI entry point is the one place the dependency is acceptable.
    from scripts._panda_env import HARDEN_HOME

    ap = argparse.ArgumentParser(description="Harden cross-run proposer journal")
    sub = ap.add_subparsers(dest="cmd", required=True)
    show = sub.add_parser("show", help="print the rendered PRIOR RUNS section")
    show.add_argument("--max-entries", type=int, default=25)
    bf = sub.add_parser("backfill", help="import a run dir's history.jsonl")
    bf.add_argument("run_dirs", nargs="+")
    bf.add_argument("--cases", default="", help="cases file label to stamp on entries")
    args = ap.parse_args()

    journal = Journal(Path(HARDEN_HOME) / DEFAULT_NAME, context=getattr(args, "cases", ""))
    if args.cmd == "show":
        print(journal.render(max_entries=args.max_entries) or "(journal empty)")
    else:
        for run_dir in args.run_dirs:
            n = backfill(run_dir, journal)
            print(f"{run_dir}: imported {n} round(s)")


if __name__ == "__main__":
    main()
